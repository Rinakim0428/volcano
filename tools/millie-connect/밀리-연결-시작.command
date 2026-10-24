#!/bin/bash
set -euo pipefail
cd -- "$(dirname -- "$0")"
case "$(uname -m)" in
  arm64) binary="bin/millie-connect-arm64" ;;
  x86_64) binary="bin/millie-connect-amd64" ;;
  *) printf '%s\n' '지원하지 않는 Mac CPU입니다.'; exit 1 ;;
esac
if [[ ! -x "$binary" ]]; then
  printf '%s\n' '실행 파일이 없거나 실행 권한을 읽지 못했습니다. ZIP 전체를 다시 압축 해제해 주세요.'
  exit 1
fi
exec "./$binary"
