#!/usr/bin/env python3
"""Attach a file to a GitHub release.

The release runner has no gh CLI, and curl's presence is not worth betting a
release on, so this speaks the REST API over the standard library. Replaces an
existing asset of the same name, making reruns idempotent.

Usage: upload-release-asset.py <tag> <file>
Environment: GITHUB_TOKEN, GITHUB_REPOSITORY
"""

import json
import os
import sys
import urllib.error
import urllib.request

API = "https://api.github.com"


def call(url, token, method="GET", data=None, content_type=None):
    request = urllib.request.Request(url, data=data, method=method)
    request.add_header("Authorization", f"Bearer {token}")
    request.add_header("Accept", "application/vnd.github+json")
    request.add_header("X-GitHub-Api-Version", "2022-11-28")
    if content_type:
        request.add_header("Content-Type", content_type)
    try:
        with urllib.request.urlopen(request) as response:
            body = response.read()
            return json.loads(body) if body else None
    except urllib.error.HTTPError as error:
        sys.exit(f"upload-release-asset: {method} {url} -> {error.code} {error.read().decode()}")


def main():
    tag, path = sys.argv[1], sys.argv[2]
    token = os.environ["GITHUB_TOKEN"]
    repo = os.environ["GITHUB_REPOSITORY"]
    name = os.path.basename(path)

    release = call(f"{API}/repos/{repo}/releases/tags/{tag}", token)

    for asset in release["assets"]:
        if asset["name"] == name:
            call(f"{API}/repos/{repo}/releases/assets/{asset['id']}", token, method="DELETE")

    with open(path, "rb") as f:
        payload = f.read()

    url = f"https://uploads.github.com/repos/{repo}/releases/{release['id']}/assets?name={name}"
    asset = call(url, token, method="POST", data=payload, content_type="application/octet-stream")
    print(f"{asset['browser_download_url']} ({asset['size']} bytes)")


if __name__ == "__main__":
    main()
