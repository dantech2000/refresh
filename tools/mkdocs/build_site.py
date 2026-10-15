"""Build the versioned docs site into site/.

The published layout under site_url (https://drod.dev/refresh/):

    v2/                  the current docs, from docs/ in this checkout
    v1/                  the docs of 0.16 and earlier, from V1_REF
    schema/v1, v2        the JSON Schemas, from api/schema (their $id points here)
    versions.json        read by the Material version selector
    index.html, <path>/  redirect stubs: every page path of v2 goes to v2,
                         a page path that only v1 has goes to v1
    404.html, sitemap.xml

Run it from the repository root with the locked environment:

    uv run --frozen python tools/mkdocs/build_site.py

Both versions build with --strict. Any failed step fails the script.
"""

import argparse
import html
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

# The last commit of the 0.16 line (it has the 0.16.1 docs fixes). The v1
# docs build from this commit and nothing else.
V1_REF = "682934ced20e71eeacaa7639e15763d2b23d5506"

VERSIONS = [
    {"version": "v2", "title": "v2 (0.17 and later)", "aliases": ["latest"]},
    {"version": "v1", "title": "v1 (0.16 and earlier)", "aliases": []},
]
CURRENT = "v2"

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
V1_OVERRIDES = os.path.join(REPO, "tools", "mkdocs", "overrides-v1")

# Top-level names that the version builds and the schemas own. A redirect
# stub must never take one of them.
RESERVED = {"v1", "v2", "schema", "versions.json", "sitemap.xml", "404.html"}


def run(args, cwd):
    print("+ " + " ".join(args), file=sys.stderr, flush=True)
    subprocess.run(args, cwd=cwd, check=True)


def site_url():
    """Read site_url from mkdocs.yml (it has Python tags, so no YAML load)."""
    with open(os.path.join(REPO, "mkdocs.yml"), encoding="utf-8") as f:
        m = re.search(r"^site_url:\s*(\S+)\s*$", f.read(), re.MULTILINE)
    if not m:
        sys.exit("build_site: mkdocs.yml has no site_url")
    url = m.group(1)
    return url if url.endswith("/") else url + "/"


def build_version(checkout, version, out_dir, base_url, tmp, custom_dir=None):
    """Build one version of the docs with an override config.

    The override INHERITs the checkout's mkdocs.yml. Paths are absolute
    because the override lives in a temp dir. hooks is empty: the schema
    hook would copy api/schema into the version dir, and the root copy is
    the one that counts.
    """
    lines = [
        f"INHERIT: {json.dumps(os.path.join(checkout, 'mkdocs.yml'))}",
        f"docs_dir: {json.dumps(os.path.join(checkout, 'docs'))}",
        f"site_dir: {json.dumps(out_dir)}",
        f"site_url: {json.dumps(base_url + version + '/')}",
        "hooks: []",
        "extra:",
        "  version:",
        "    provider: mike",
    ]
    if custom_dir:
        lines += ["theme:", f"  custom_dir: {json.dumps(custom_dir)}"]
    cfg = os.path.join(tmp, f"mkdocs-{version}.yml")
    with open(cfg, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    # pymdownx.snippets reads base_path "." from the working directory.
    run([sys.executable, "-m", "mkdocs", "build", "--strict", "-f", cfg], cwd=checkout)


def ensure_commit(ref):
    """Fetch V1_REF when the checkout is shallow and does not have it."""
    have = subprocess.run(
        ["git", "cat-file", "-e", ref + "^{commit}"],
        cwd=REPO,
        stderr=subprocess.DEVNULL,
    ).returncode == 0
    if not have:
        run(["git", "fetch", "--no-tags", "--depth=1", "origin", ref], cwd=REPO)


def page_paths(site_dir):
    """Return the page paths of a built site: every dir with an index.html."""
    paths = set()
    for root, _, files in os.walk(site_dir):
        if "index.html" in files:
            rel = os.path.relpath(root, site_dir)
            paths.add("" if rel == "." else rel.replace(os.sep, "/") + "/")
    return paths


def stub(target_path, canonical):
    t = html.escape(target_path, quote=True)
    c = html.escape(canonical, quote=True)
    js = json.dumps(target_path)
    return (
        "<!doctype html>\n"
        '<html lang="en">\n'
        "<head>\n"
        '<meta charset="utf-8">\n'
        "<title>Redirecting</title>\n"
        f'<link rel="canonical" href="{c}">\n'
        '<meta name="robots" content="noindex">\n'
        f"<script>location.replace({js} + location.search + location.hash)</script>\n"
        f'<meta http-equiv="refresh" content="0; url={t}">\n'
        "</head>\n"
        f'<body><p>This page moved to <a href="{t}">{t}</a>.</p></body>\n'
        "</html>\n"
    )


def write_redirects(site, base_url, pages):
    """Write a stub for every page path; v2 wins when both versions have it."""
    base_path = re.sub(r"^https?://[^/]+", "", base_url)
    for version in ["v1", CURRENT]:  # CURRENT last, so it overwrites
        for path in sorted(pages[version]):
            top = path.split("/", 1)[0]
            if top in RESERVED:
                sys.exit(f"build_site: page {version}/{path} collides with a reserved name")
            out = os.path.join(site, path, "index.html")
            os.makedirs(os.path.dirname(out), exist_ok=True)
            with open(out, "w", encoding="utf-8") as f:
                f.write(stub(f"{base_path}{version}/{path}", f"{base_url}{version}/{path}"))
    return len(pages["v1"] | pages[CURRENT])


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--site-dir", default=os.path.join(REPO, "site"))
    args = parser.parse_args()
    site = os.path.abspath(args.site_dir)
    base_url = site_url()

    shutil.rmtree(site, ignore_errors=True)
    os.makedirs(site)

    with tempfile.TemporaryDirectory(prefix="refresh-docs-") as tmp:
        # 1. The current docs.
        build_version(REPO, CURRENT, os.path.join(site, CURRENT), base_url, tmp)

        # 2. The 0.16 docs, from a worktree of V1_REF.
        ensure_commit(V1_REF)
        v1_src = os.path.join(tmp, "v1-src")
        run(["git", "worktree", "add", "--detach", v1_src, V1_REF], cwd=REPO)
        try:
            build_version(v1_src, "v1", os.path.join(site, "v1"), base_url, tmp, V1_OVERRIDES)
        finally:
            run(["git", "worktree", "remove", "--force", v1_src], cwd=REPO)

    # 3. The schemas, at the URLs of their $id.
    for name in sorted(os.listdir(os.path.join(REPO, "api", "schema"))):
        shutil.copytree(
            os.path.join(REPO, "api", "schema", name),
            os.path.join(site, "schema", name),
        )

    # 4. versions.json, the redirect stubs, the root 404 page and sitemap.
    with open(os.path.join(site, "versions.json"), "w", encoding="utf-8") as f:
        json.dump(VERSIONS, f, indent=2)
        f.write("\n")

    pages = {v["version"]: page_paths(os.path.join(site, v["version"])) for v in VERSIONS}
    count = write_redirects(site, base_url, pages)

    # The 404 page uses absolute URLs (site_url), so it works at the root.
    shutil.copy(os.path.join(site, CURRENT, "404.html"), os.path.join(site, "404.html"))

    # The root sitemap points at the current version only.
    with open(os.path.join(site, "sitemap.xml"), "w", encoding="utf-8") as f:
        f.write(
            '<?xml version="1.0" encoding="UTF-8"?>\n'
            '<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n'
            f"  <sitemap><loc>{base_url}{CURRENT}/sitemap.xml</loc></sitemap>\n"
            "</sitemapindex>\n"
        )

    print(
        f"build_site: {site}: v2 {len(pages['v2'])} pages, v1 {len(pages['v1'])} pages, "
        f"{count} redirect stubs",
        file=sys.stderr,
    )


if __name__ == "__main__":
    main()
