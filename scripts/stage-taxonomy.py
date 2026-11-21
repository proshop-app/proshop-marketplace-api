#!/usr/bin/env python3
"""Готовит проверенный артефакт для go:embed из локального файла или checkout Uno."""

import hashlib
import json
import os
import sys
import tempfile
from pathlib import Path


DESTINATION = (
    Path(__file__).resolve().parents[1]
    / "internal/infrastructure/taxonomydata/taxonomy-bundle.json"
)


def stage(source: Path) -> None:
    pin = json.loads(DESTINATION.with_name("release.lock.json").read_text(encoding="utf-8"))
    if source.is_dir():
        source = source / "releases" / f"taxonomy-v{pin['release']}.json"
    body = source.read_bytes()
    digest = hashlib.sha256(body).hexdigest()
    if digest != pin["sha256"]:
        raise ValueError(f"{source}: SHA-256 {digest}, ожидался {pin['sha256']}")
    if DESTINATION.is_file() and DESTINATION.read_bytes() == body:
        return

    with tempfile.NamedTemporaryFile(dir=DESTINATION.parent, delete=False) as temporary:
        path = Path(temporary.name)
        try:
            temporary.write(body)
            temporary.close()
            os.replace(path, DESTINATION)
        finally:
            path.unlink(missing_ok=True)


if __name__ == "__main__":
    try:
        stage(Path(sys.argv[1]))
    except (OSError, ValueError) as error:
        sys.exit(f"Поставка таксономии: {error}. Укажите UNO_REFERENCE_DATA_DIR или TAXONOMY_BUNDLE.")
