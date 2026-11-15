# Proposal: add-linux-arm64

## Why

Агент собирается и поставляется только под amd64, а ARM64 — второй по
распространённости таргет для self-hosted Linux (Raspberry Pi, ARM VPS типа
Oracle/Ampere, мини-серверы). Код чисто-Go без CGO и архитектурных
зависимостей — сборка `linux/arm64` проходит с первого раза, так что
расширение покрытия стоит только сборочной обвязки и доков. Делаем сейчас,
пока релиз v0.4.0 не опубликован (GitHub-аккаунт заблокирован): переименование
артефактов ничего не ломает.

## What Changes

- **Третья цель сборки** в `build.ps1`: `linux/arm64` → `bin/siren-linux-arm64`
  (статический, `CGO_ENABLED=0`). Матрица из трёх целей собирается всегда,
  без параметров.
- **BREAKING (только имена, содержимое не меняется):** явная архитектура во
  всех артефактах: `siren-win-kit.zip` → `siren-windows-amd64-kit.zip`,
  `siren-linux-kit.zip` → `siren-linux-amd64-kit.zip`, новый
  `siren-linux-arm64-kit.zip`. Бинарники уже имели суффиксы — не меняются.
- **Консолидация исходников кита**: `install.sh`/`uninstall.sh` сейчас живут
  только в gitignored `dist/` (в репо их нет) — канонические копии переезжают
  в `deploy/`, киты собираются из них.
- **`install.sh` проверяет архитектуру машины** (`uname -m`) против
  ожидаемой для кита: `x86_64` для amd64-кита, `aarch64` для arm64-кита.
- Доки: `docs/deployment.md` (сборка трёх целей, ARM-установка),
  `README.md` (quick start), `deploy/README.md`, `deploy/acceptance.md`.

Windows ARM64 — вне скоупа (отклонено: walk-пульт на arm64 в рантайме не
обкатан, сценарий покрывается эмуляцией amd64).

## Capabilities

### New Capabilities

(нет — поведение агента не меняется: спеки arch-агностичны)

### Modified Capabilities

(нет — change чисто реализационный: сборка, комплекты, доки; `skip_specs: true`)

## Impact

- `build.ps1` — третья цель, переименование выводов.
- `deploy/` — новые канонические `install.sh`, `uninstall.sh` (перенос из
  dist-кита с правкой проверки архитектуры).
- `dist/` (gitignored) — переименованные каталоги китов + новый arm64-кит,
  пересборка zip.
- Доки: `docs/deployment.md`, `README.md`, `deploy/README.md`,
  `deploy/acceptance.md`.
- Runtime-код Go не меняется; конфиг не меняется; спеки не меняются.
