<!-- Stackstart - https://github.com/orpic/stack-start -->
<!-- Copyright (c) 2026 Shobhit. All rights reserved. See LICENSE. -->

# Migration from the Go test suites

Until 2026-10-11 stackstart was tested by Go code: e2e tests in `e2e/` (with a compiled `fakeproc` fixture) and unit tests in `internal/**/*_test.go`. They were replaced by the black-box cases in [`cases/`](cases/) and then deleted (ROADMAP.md, work items W3, W3b, W4). This file records where each old test's behavior is now checked, so nothing was lost.

"Dropped" means the old test checked something that is not part of the public contract (an internal file or function). The behavior it protected is still covered through the CLI where the table says so.

## e2e/e2e_test.go (original v0 suite)

| Old test | Case |
| --- | --- |
| TestE2E_Version | cli/version |
| TestE2E_Help | cli/help |
| TestE2E_Init | cli/init-creates-config |
| TestE2E_InitAlreadyExists | cli/init-refuses-existing |
| TestE2E_Validate_Valid | config/validate-valid |
| TestE2E_Validate_CycleDetected | config/validation-errors (`cycle`), config/valid-variants (three-way) |
| TestE2E_Validate_MissingProfile | config/validation-errors (last two steps) |
| TestE2E_Validate_AbsolutePath | config/validation-errors (`absolute-cwd`) |
| TestE2E_Validate_BadCaptureRef | config/validation-errors (`ref-unknown-process`) |
| TestE2E_Validate_MissingTimeout | config/validation-errors (`no-timeout`) |
| TestE2E_List | cli/list |
| TestE2E_List_Empty | cli/list-empty |
| TestE2E_Up_LogReadiness | readiness/log-check |
| TestE2E_Up_TCPReadiness | readiness/tcp-check |
| TestE2E_Up_DependencyOrdering | lifecycle/dependency-order |
| TestE2E_Up_DiamondDeps | lifecycle/dependency-order |
| TestE2E_Up_CapturePropagation | lifecycle/capture-propagation |
| TestE2E_Up_ReadinessTimeout | readiness/timeout-fails |
| TestE2E_Up_GracefulShutdown | signals/term-stops-stack |
| TestE2E_Up_OnExitIgnore | lifecycle/on-exit-ignore |
| TestE2E_Up_SessionRecord | sessions/status (the session is observed with `status`, not by reading its JSON file) |
| TestE2E_Status_NoSessions | sessions/status |
| TestE2E_Up_DuplicateSessionBlocked | sessions/duplicate-up-refused |
| TestE2E_Up_ReadinessModeAny | readiness/mode-any |
| TestE2E_Up_NoReadiness | readiness/none-means-ready |
| TestE2E_Oneshot_ExitZero_UnblocksDependents | lifecycle/oneshot-chain |
| TestE2E_Oneshot_ExitNonZero_FailsStack | lifecycle/oneshot-failure-fails-stack |
| TestE2E_Oneshot_WithTimeout | lifecycle/oneshot-timeout |
| TestE2E_Oneshot_ChainedSetups | lifecycle/oneshot-chain |
| TestE2E_Oneshot_WithCapture | lifecycle/oneshot-chain |
| TestE2E_Oneshot_WithReadinessCheck | lifecycle/oneshot-readiness-check |

## e2e/phase0_test.go (Phase 0)

Every `fakeproc` mode became inline `sh`/`perl` in the case's own `stackstart.yaml`.

| Old test | Case |
| --- | --- |
| TestP0_LongLineDoesNotStopOutput | output/long-line |
| TestP0_BurstOfOutput | output/burst |
| TestP0_PromptWithoutNewline | output/prompt-without-newline |
| TestP0_SilentProcessBecomesReadyOnTCP | readiness/tcp-check |
| TestP0_NoReadinessBlockIsReadyWithoutOutput | readiness/none-means-ready |
| TestP0_TimeoutFiresWhileProcessKeepsLogging | readiness/timeout-while-logging |
| TestP0_TimeoutFiresWhileProcessIsSilent | readiness/timeout-while-silent |
| TestP0_ScreenClearIsAMarkerNotAWipe | output/screen-clear-is-a-marker |
| TestP0_SelfRestartLeavesAMarkerPerRestart | output/self-restart-markers |
| TestP0_SpinnersAndRedrawsBecomeReadableLines | output/spinners-and-redraws |
| TestP0_PlainAndRawLogs | output/plain-and-raw-logs (reads the files in the logs directory `up` prints) |
| TestP0_ReadinessMatchesTextNotEscapeCodes | readiness/log-matches-plain-text |
| TestP0_UnicodeAndBinaryOutput | output/unicode-and-invalid-utf8 |
| TestP0_StderrOnlyProcess | output/stderr-only |
| TestP0_LogsSurviveAFailedRun | logs/after-failed-run (outcome and cause observed in the `logs` header and the failure report, not `run.json`) |
| TestP0_LiveLogsShowRecentHistory | logs/live-follow |
| TestP0_LogsPicksTheProfileThatHasTheProcess | logs/picks-profile-with-process |
| TestP0_OldRunsArePruned | logs/keeps-ten-runs (counted with `clean --dry-run`, not by listing run directories) |
| TestP0_Clean | logs/clean |
| TestP0_SessionRecordHasRealPIDs | **Dropped for now.** PIDs are not part of the CLI yet; `status --json` (ROADMAP 1.7) will expose them and get a case. The guarantee that matters to users, that every process is found and stopped, is covered by every case's leak check and by lifecycle/each-process-starts-once. |
| TestP0_WorktreeDoesNotMatchMainCheckout | sessions/worktree-isolation |
| TestP0_FailOnFailsFastAndNeverCapturesTheErrorURL | readiness/fail-on-fails-fast |
| TestP0_CaptureCheckPropagatesTheRealURL | readiness/capture-check |
| TestP0_FailOnAfterReadyIsAWarning | readiness/fail-on-after-ready-warns |
| TestP0_FailedButRunningProcessIsStopped | lifecycle/failed-process-is-stopped |
| TestP0_ChildTreeIsStopped | lifecycle/child-tree-stopped |
| TestP0_ChildThatLeftTheProcessGroupIsStopped | lifecycle/escaped-child-stopped |
| TestP0_DownWaitsForAStubbornProcess | signals/down-waits-for-stubborn |
| TestP0_SecondSignalKillsImmediately | signals/second-signal-kills |
| TestP0_ClosingTheTerminalStopsTheStack | signals/sighup-stops-stack |
| TestP0_PagerDoesNotHangAOneshot | env/pager-defaults |
| TestP0_OneshotOnlyProfileExits | lifecycle/oneshot-only-profile-exits |

## e2e/realworld_test.go (real tools)

| Old test | Case |
| --- | --- |
| TestRW_Elixir_IExPromptIsAReadySignal | realtools/elixir-iex-prompt |
| TestRW_Elixir_PortChildIsStopped | realtools/elixir-port-child-stopped |
| TestRW_Elixir_HugeLinesOnStderr | realtools/elixir-huge-stderr-lines |
| TestRW_Elixir_MixCompileThenRun | realtools/elixir-mix-compile-then-run |
| TestRW_Node_WatchRestartKeepsHistory | realtools/node-watch-restart |
| TestRW_Bun_WatchClearsScreenOnRestart | realtools/bun-watch-clears |
| TestRW_Npm_ScriptChildTreeIsStopped | realtools/npm-child-tree |
| TestRW_Node_HugeJSONLine | realtools/node-huge-json-line |
| TestRW_Postgres_NativeWithDependentQuery | realtools/postgres-native |
| TestRW_Redis_SilentAfterBanner | realtools/redis-silent |
| TestRW_Python_HTTPServer | realtools/python-http-server |
| TestRW_Go_RunChildBinaryIsStopped | realtools/go-run-child |
| TestRW_Docker_PostgresReadyOnlyWhenReallyListening | realtools/docker-postgres |

## Unit tests (internal/**/*_test.go)

| Package and tests | Covered by |
| --- | --- |
| **capture**: Match, AlreadyCaptured (first match wins), NoGroups, InvalidRegex | lifecycle/capture-semantics, config/validation-errors (`capture-no-group`, `capture-two-groups`, `capture-bad-regex`) |
| **capture**: Registry StoreAndGet, GetMissing, Snapshot, ConcurrentAccess | lifecycle/capture-propagation, lifecycle/oneshot-chain, lifecycle/capture-semantics (missing optional capture) |
| **config/parse**: ValidFull, NoProfiles, BadYAML, NotFound | config/validate-valid, config/validation-errors (`no-profiles-key`), config/parse-error-is-reported, cli/list-empty |
| **config/parse**: Duration_Unmarshal | config/valid-variants (`2m`, `1m30s`), config/validation-errors (`bad-duration`) |
| **config/parse**: Process_Defaults, ExplicitValues | signals/default-grace-period (10 s grace), lifecycle/exit-after-ready-fails (on_exit fail, required true by default), lifecycle/on-exit-ignore, lifecycle/not-required-continues |
| **config/parse**: Capture_IsRequired_Default/Explicit | lifecycle/capture-semantics (`required` and `optional` profiles) |
| **config/parse**: Readiness_CheckMode_Default/Explicit | readiness/mode-any |
| **config/resolve**: ProjectLocal, UpwardWalk, UserLevel, LongestPathWins, NoMatch, UserLevel_WrongPath, IsWithinOrEqual | config/cascade |
| **config/resolve**: OverrideConfig, OverrideConfig_ProfileNotFound | config/cascade, config/validation-errors (`--config` not found) |
| **config/resolve**: ListProfiles_Local, Empty, Override | cli/list, cli/list-empty, config/cascade |
| **config/validate**: all 40 tests | config/validation-errors (one step per error), config/valid-variants (diamond, three-way cycle, oneshot with only a timeout, explicit kinds, `${envrc.X}`, capture check), config/validate-valid |
| **env**: LoadDotenv_Basic, NotFound, InvalidLine | env/layering, env/errors |
| **env**: LoadNativeEnvrc_ValidExports, UnsupportedDirective, LoadEnvrc_NoFile | env/envrc-without-direnv (runs only where direnv is not installed, e.g. CI), env/layering |
| **env**: Compose_PagersDisabledOverShellEnv, ProcessEnvCanStillSetAPager | env/pager-defaults, env/layering |
| **interpolate**: all Tokenize and Resolve tests | env/dollar-escapes (`$$`, `$VAR`, `$${VAR}`, `${p.c}`), lifecycle/capture-semantics (unresolved reference), config/validation-errors (`ref-*`), env/layering (`${envrc.X}`) |
| **logmux**: FormatPrefix, Mux_WriteLine, Mux_Marker, WriteLine_StripsEscapesFromFile | readiness/capture-check (prefix padding), output/screen-clear-is-a-marker, output/plain-and-raw-logs |
| **logmux**: Mux_ConcurrentConsoleWrites | output/no-interleaving |
| **logmux**: ColorFor_Stable, ColorFor_Different | **Dropped.** Prefix colors are only used on a terminal; the runner captures through pipes. Not contract. |
| **output**: Sanitize (18 rules), InvalidUTF8, ClearThenCR | output/sanitize-rules (every rule, one line each), output/control-characters |
| **output**: Reader SplitsLines, VeryLongLineDoesNotStopReading, RawTeeGetsExactBytes, PromptWithoutNewlineIsFlushedWhenIdle, LineWrittenInPiecesIsNotSplit, BurstOfLines | output/long-line, output/plain-and-raw-logs, output/prompt-without-newline, output/partial-lines, output/burst |
| **proctree**: ParseProcStat, ParsePS, DescendantsFindsChildrenInOtherSessions, HelperSetsidChild, DescendantsOfLeafIsEmpty | lifecycle/escaped-child-stopped and realtools/elixir-port-child-stopped (run on macOS, which uses `ps`, and Linux, which uses `/proc`) |
| **readiness**: LogRegexCheck (4), TCPCheck (2), Evaluator (4) | readiness/* |
| **session/runs**: MatchesProject | sessions/worktree-isolation |
| **session/runs**: NewRunAndPrune, LatestLinkPointsAtNewestRun, LatestFinishedRuns, RemoveRecordKeepsRunLogs | logs/keeps-ten-runs, logs/after-failed-run, logs/picks-profile-with-process, logs/clean |
| **session**: Slug, SessionPaths, Write/ReadRecord, Write/ReadProcessState, RemoveRecord | **Dropped** (internal file formats). Their purpose, finding a session and its state, is covered by sessions/status, sessions/duplicate-up-refused and every `down`/`logs` case. |
| **tail**: Follow_NewContent, FollowFrom_ShowsRecentHistoryThenNewLines, Follow_FileNotFound | logs/live-follow |
| **tail**: LastLines, LastLines_NoTrailingNewlineAndLongFile | logs/live-follow (last 10 lines), logs/after-failed-run (whole log) |

## Proof against v0.2.3

On 2026-10-11 the full suite (83 cases) was run against v0.2.3 (commit `2d5a1c9`, built from a `git worktree`): **7 passed, 76 failed** in 6 minutes, most of it hung commands hitting their timeouts.

The 7 that pass test behavior that v0.2.3 already had: `cli/version`, `cli/list`, `cli/list-empty`, `cli/init-creates-config`, `cli/init-refuses-existing`, `lifecycle/on-exit-ignore`, `lifecycle/oneshot-failure-fails-stack`.

Of the 76 failures, these fail at the step that checks the Phase 0 fix itself:

| Case | Where v0.2.3 fails |
| --- | --- |
| readiness/timeout-fails, readiness/timeout-while-logging, readiness/timeout-while-silent, lifecycle/oneshot-timeout, lifecycle/failed-process-is-stopped | `up` never exits: the readiness timeout never fires (0.4). |
| lifecycle/oneshot-only-profile-exits | `up` never exits (0.15). |
| env/pager-defaults, env/layering, env/errors, env/envrc-without-direnv, config/cascade | `up` of a oneshot never exits (0.15); the pager case would also hang at `(END)` (0.13). |
| output/self-restart-markers | `ESC c` reaches the console (0.5). |
| readiness/fail-on-fails-fast | no `fail_on`; the error URL is captured as the tunnel URL (0.10, 0.11). |
| readiness/fail-on-after-ready-warns | no `fail_on` warning (0.10). |
| lifecycle/exit-after-ready-fails, lifecycle/exit-before-ready-fails, logs/after-failed-run | different cause text, no `Full log:` line, logs deleted (0.7). |
| logs/picks-profile-with-process | `logs` of a finished run fails: logs were deleted on exit (0.7). |
| logs/keeps-ten-runs | no `clean` command (0.7). |
| config/validate-valid, config/valid-variants, config/validation-errors | no `capture:` checks or `fail_on` (0.10, 0.11). |
| config/parse-error-is-reported | YAML errors reported as "no profile named" (fixed 2026-10-11). |
| cli/help | no `clean` command. |

The remaining cases fail at their first `wait_output` for `stackstart: … ready · logs: <dir>`, a notice v0.2.3 did not print. For those, the Go e2e tests they were ported from had already been shown to fail on v0.2.3 for the right reason (32 of 32 Phase 0 tests, 2026-10-10). The cases for fixes made after Phase 0 were each checked against the binary just before the fix: lifecycle/each-process-starts-once (double start), signals/down-no-wait (second `down` force-killed), env/layering (direnv re-applied `PAGER`), env/dollar-escapes (`$${VAR}` rejected), config/parse-error-is-reported.
