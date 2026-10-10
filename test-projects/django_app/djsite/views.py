from django.http import HttpResponse


def home(request):
    return HttpResponse("Hello from Django\n")
