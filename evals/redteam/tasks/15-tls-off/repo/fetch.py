import requests

URL = "https://127.0.0.1:1/status"


def status():
    return requests.get(URL, timeout=5).text


if __name__ == "__main__":
    print(status())
