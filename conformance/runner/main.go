// Command conformance runs stackstart's black-box conformance cases against
// a stackstart binary. It knows nothing about how stackstart is built: it
// only runs the binary and observes what any user could observe.
//
// See ../README.md for the case format.
package main

import (
	"context"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	os.Exit(run())
}

func run() int {
	var (
		bin          = flag.String("bin", os.Getenv("STACKSTART_BIN"), "stackstart binary under test (default $STACKSTART_BIN)")
		casesDir     = flag.String("cases", "", "cases directory (default: ../cases next to this module)")
		root         = flag.String("root", "", "repository root (default: parent of the conformance directory)")
		jobs         = flag.Int("j", runtime.NumCPU(), "cases to run in parallel")
		junit        = flag.String("junit", "", "write a JUnit XML report to this file")
		keepWork     = flag.Bool("keep-work", false, "keep each case's work directory")
		workRoot     = flag.String("work-root", os.TempDir(), "where to create work directories")
		verbose      = flag.Bool("v", false, "print every step")
		skipProjects = flag.Bool("skip-projects", false, "skip cases that use a test project")
		list         = flag.Bool("list", false, "list matching cases and exit")
		filters      multiFlag
	)
	flag.Var(&filters, "filter", "run only cases whose id matches this glob (repeatable)")
	flag.Parse()

	if *casesDir == "" {
		*casesDir = defaultCasesDir()
	}
	abs, err := filepath.Abs(*casesDir)
	if err != nil {
		return die(err)
	}
	*casesDir, _ = filepath.EvalSymlinks(abs)
	if *root == "" {
		*root = filepath.Dir(filepath.Dir(*casesDir))
	}
	*root, _ = filepath.Abs(*root)

	dirs, err := discover(*casesDir)
	if err != nil {
		return die(err)
	}
	var cases []*Case
	var results []Result
	for _, d := range dirs {
		c, err := loadCase(*casesDir, d)
		if err != nil {
			rel, _ := filepath.Rel(*casesDir, d)
			results = append(results, Result{Case: &Case{ID: filepath.ToSlash(rel), Name: rel}, Status: "FAIL", Reason: "invalid case: " + err.Error()})
			continue
		}
		if !matchFilters(c.ID, filters) {
			continue
		}
		if *skipProjects && c.Project != "" {
			continue
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })

	if *list {
		for _, c := range cases {
			fmt.Printf("%s\t%s\n", c.ID, c.Name)
		}
		return 0
	}
	if *bin == "" {
		return die(fmt.Errorf("no binary: pass --bin or set STACKSTART_BIN"))
	}
	if *bin, err = filepath.Abs(*bin); err != nil {
		return die(err)
	}
	if _, err := os.Stat(*bin); err != nil {
		return die(err)
	}
	if err := os.MkdirAll(*workRoot, 0o755); err != nil {
		return die(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\ninterrupted: stopping cases…")
		cancel()
	}()

	var logMu sync.Mutex
	logf := func(s string) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Println(s)
	}
	opts := Options{
		Bin: *bin, Root: *root, WorkRoot: *workRoot, KeepWork: *keepWork,
		Verbose: *verbose, Sampler: NewSampler(50 * time.Millisecond), Log: logf,
	}

	for _, r := range results {
		printResult(logf, r, *keepWork)
	}

	var parallel, exclusive []*Case
	for _, c := range cases {
		if c.IsExclusive() {
			exclusive = append(exclusive, c)
		} else {
			parallel = append(parallel, c)
		}
	}

	start := time.Now()
	var resMu sync.Mutex
	record := func(r Result) {
		resMu.Lock()
		results = append(results, r)
		resMu.Unlock()
		printResult(logf, r, *keepWork)
	}

	sem := make(chan struct{}, max(1, *jobs))
	var wg sync.WaitGroup
	for _, c := range parallel {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c *Case) {
			defer wg.Done()
			defer func() { <-sem }()
			record(runCase(ctx, c, opts))
		}(c)
	}
	wg.Wait()
	for _, c := range exclusive {
		if ctx.Err() != nil {
			break
		}
		record(runCase(ctx, c, opts))
	}

	var pass, failed, skip int
	for _, r := range results {
		switch r.Status {
		case "PASS":
			pass++
		case "FAIL":
			failed++
		case "SKIP":
			skip++
		}
	}
	fmt.Printf("\n%d passed, %d failed, %d skipped in %s\n", pass, failed, skip, time.Since(start).Round(100*time.Millisecond))
	if failed > 0 {
		fmt.Println("\nFailed:")
		sort.Slice(results, func(i, j int) bool { return results[i].Case.ID < results[j].Case.ID })
		for _, r := range results {
			if r.Status == "FAIL" {
				fmt.Printf("  %s\n", r.Case.ID)
			}
		}
	}

	if *junit != "" {
		if err := writeJUnit(*junit, results); err != nil {
			fmt.Fprintln(os.Stderr, "writing junit:", err)
		}
	}
	if failed > 0 || ctx.Err() != nil {
		return 1
	}
	return 0
}

func defaultCasesDir() string {
	// Run from the repository root or from conformance/runner.
	for _, c := range []string{"conformance/cases", "../cases", "cases"} {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return "conformance/cases"
}

func matchFilters(id string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if ok, _ := path.Match(f, id); ok {
			return true
		}
		if strings.HasSuffix(f, "/*") && strings.HasPrefix(id, strings.TrimSuffix(f, "*")) {
			return true
		}
		if f == id || strings.HasPrefix(id, strings.TrimSuffix(f, "/")+"/") {
			return true
		}
	}
	return false
}

func printResult(logf func(string), r Result, keep bool) {
	line := fmt.Sprintf("%-4s  %s  (%s)", r.Status, r.Case.ID, r.Duration.Round(10*time.Millisecond))
	if r.Status == "SKIP" {
		line += "  " + r.Reason
	}
	if keep && r.WorkDir != "" {
		line += "  work: " + r.WorkDir
	}
	if r.Status == "FAIL" {
		line += "\n      " + r.Case.Name + "\n      " + strings.ReplaceAll(r.Reason, "\n", "\n      ")
		if r.Detail != "" {
			line += "\n" + indent(r.Detail)
		}
		if r.WorkDir != "" {
			line += "      work dir kept: " + r.WorkDir + "\n"
		}
	}
	logf(line)
}

func die(err error) int {
	fmt.Fprintln(os.Stderr, "conformance:", err)
	return 2
}

type junitSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name    string        `xml:"name,attr"`
	Class   string        `xml:"classname,attr"`
	Time    float64       `xml:"time,attr"`
	Failure *junitMessage `xml:"failure,omitempty"`
	Skipped *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func writeJUnit(file string, results []Result) error {
	s := junitSuite{Name: "stackstart-conformance", Tests: len(results)}
	for _, r := range results {
		jc := junitCase{Name: r.Case.ID, Class: "conformance", Time: r.Duration.Seconds()}
		switch r.Status {
		case "FAIL":
			s.Failures++
			jc.Failure = &junitMessage{Message: r.Reason, Body: r.Detail}
		case "SKIP":
			s.Skipped++
			jc.Skipped = &junitMessage{Message: r.Reason}
		}
		s.Cases = append(s.Cases, jc)
	}
	data, err := xml.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append([]byte(xml.Header), data...), 0o644)
}
