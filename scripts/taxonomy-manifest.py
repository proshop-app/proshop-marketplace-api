"""Проверить манифест исходных данных или выпустить следующий номер поставки."""
import argparse
import hashlib
import json
from pathlib import Path

DATA = Path(__file__).resolve().parents[1] / 'internal/infrastructure/taxonomydata/data'
FILES = ('attributes.json', 'schemas.json', 'categories.json')
MAX_VERSION = 9007199254740991


def describe():
    payloads = {name: (DATA / name).read_bytes() for name in FILES}
    definitions = json.loads(payloads['attributes.json'])
    schemas = json.loads(payloads['schemas.json'])
    roots = json.loads(payloads['categories.json'])
    nodes, leaves = [], []

    def walk(categories):
        for category in categories:
            nodes.append(category)
            if category.get('children'):
                walk(category['children'])
            else:
                leaves.append(category)

    walk(roots)
    codes = {item['code'] for item in definitions}
    if len(codes) != len(definitions) or len({item['id'] for item in nodes}) != len(nodes):
        raise ValueError('повтор идентификатора категории или кода атрибута')
    if {item['schema'] for item in leaves} != set(schemas):
        raise ValueError('неполное покрытие схемами')
    if {code for schema in schemas.values() for code in schema['attributes']} != codes:
        raise ValueError('неполные привязки атрибутов')
    for schema in schemas.values():
        if not set(schema['required']) <= set(schema['attributes']):
            raise ValueError('неизвестное обязательное поле')
    checksum = hashlib.sha256()
    for name, body in payloads.items():
        checksum.update(name.encode() + b'\x00')
        checksum.update(body)
    return {
        'checksum': checksum.hexdigest(),
        'coverage': {
            'directions': len(roots),
            'categories': len(nodes),
            'leaves': len(leaves),
            'schemas': len(schemas),
            'attributes': len(definitions),
            'values': sum(len(item.get('values', [])) for item in definitions),
        },
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', type=int, help='новый номер при изменении данных')
    parser.add_argument('--write', action='store_true', help='записать новый манифест')
    args = parser.parse_args()
    path = DATA / 'manifest.json'
    current = json.loads(path.read_text())
    result = {'release': args.release if args.release is not None else current['release'], **describe()}
    if not 1 <= result['release'] <= MAX_VERSION:
        parser.error('номер поставки вне допустимых границ')
    if args.write:
        if result != current and result['release'] <= current['release']:
            parser.error('изменение опубликованной поставки запрещено: задайте следующий --release')
        path.write_text(json.dumps(result, ensure_ascii=False, indent=4) + '\n')
    elif result != current:
        parser.error('манифест расходится с данными; подготовьте следующий номер поставки')
    print(json.dumps(result, ensure_ascii=False))


if __name__ == '__main__':
    main()
