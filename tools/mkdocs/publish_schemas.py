"""MkDocs hook: publish the JSON Schemas with the site.

The schemas are generated into api/schema/ (see tools/genschema). The site
serves them at <site_url>/schema/<version>/<Kind>.json, which is the $id of
each schema. This hook copies api/schema/ into the built site, so the docs
tree holds no second copy that could go stale.
"""

import os
import shutil


def on_post_build(config, **kwargs):
    src = os.path.join(os.path.dirname(config["config_file_path"]), "api", "schema")
    dst = os.path.join(config["site_dir"], "schema")
    if os.path.isdir(src):
        shutil.copytree(src, dst, dirs_exist_ok=True)
