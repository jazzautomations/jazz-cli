import requests

def titulo(html):
    import re
    m = re.search(r"<title>(.*?)</title>", html, re.S | re.I)
    return m.group(1).strip() if m else None

if __name__ == "__main__":
    print(titulo(requests.get("https://example.com").text))
