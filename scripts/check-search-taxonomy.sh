#!/usr/bin/env bash
# Проверяет реальные запросы текущей поставки валидатором соседнего sc-search.
set -euo pipefail
marketplace_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
search_dir=${1:-"$marketplace_dir/../sc-search"}
contract_dir=$(mktemp -d)
trap 'rm -rf -- "$contract_dir"' EXIT
export TAXONOMY_CONTRACT_DIR="$contract_dir"
cd -- "$marketplace_dir"
go test -count=1 ./internal/infrastructure/searchapi -run '^TestReleaseSearchLimits$' -v
cd -- "$search_dir"
go test -count=1 ./internal/common/marketplace ./internal/modules/admincandidates -run 'TestTaxonomyContractCorpus' -v
