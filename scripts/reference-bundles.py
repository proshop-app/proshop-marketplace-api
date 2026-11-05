#!/usr/bin/env python3
"""Собирает воспроизводимые поставки реестра из исходных словарей."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil


def clean(value):
    if isinstance(value, dict):
        return {k: clean(v) for k, v in value.items() if v is not None}
    if isinstance(value, list):
        return [clean(v) for v in value if v is not None]
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('output', type=Path, nargs='?', default=Path('deploy/reference'))
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    for name in ('brand', 'country', 'material', 'closed'):
        data = json.loads((args.source / f'{name}.v1.json').read_text())
        for dictionary in data if isinstance(data, list) else [data]:
            target = args.output / dictionary['code']
            target.mkdir(parents=True, exist_ok=True)
            entries = []
            for entry in dictionary['entries']:
                metadata = dict(entry.get('meta') or {})
                metadata.update({k: entry.get(k) for k in ('label', 'order', 'directions')})
                entries.append(clean(dict(
                    canonical=entry['value'],
                    status='confirmed' if dictionary['kind'] == 'closed' else entry.get('status', 'confirmed'),
                    aliases=entry['aliases'], metadata=metadata,
                )))
            body = (json.dumps(entries, ensure_ascii=False, indent=2) + '\n').encode()
            (target / 'entries.json').write_bytes(body)
            manifest = dict(dictionary=dictionary['code'], release=dictionary['version'],
                            checksum=hashlib.sha256(body).hexdigest(), entries=len(entries),
                            aliases=sum(len(e['aliases']) for e in entries))
            (target / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
            shutil.copyfile(args.source / f'{name}.provenance.json', target / 'provenance.json')
            if (args.source / f'{name}.LICENSE').exists():
                shutil.copyfile(args.source / f'{name}.LICENSE', target / 'LICENSE')
    shutil.copyfile(args.source / 'normalizer-v1.json', args.output / 'normalizer-v1.json')


if __name__ == '__main__':
    main()
