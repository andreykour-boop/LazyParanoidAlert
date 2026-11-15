# Tasks

## 1. Сборка и киты

- [x] 1.1 `build.ps1` — третья цель `linux/arm64` (`bin/siren-linux-arm64`), матрица из трёх целей без параметров. Проверка: `build.ps1` собирает все три артефакта, `linux/arm64` бинарник статический (`file`/заголовок ELF aarch64), существующие amd64-цели не изменились
- [x] 1.2 Консолидация исходников кита: канонические `deploy/install.sh` и `deploy/uninstall.sh` (перенос из dist-кита); в `install.sh` — проверка `uname -m` против `EXPECTED_ARCH` из шапки (`x86_64`/`aarch64`), fail-fast с сообщением; шапки «Linux amd64» → «Linux (amd64/arm64)». Проверка: в репо есть tracked-копии, dist-киты собираются из них
- [x] 1.3 Переструктуризация `dist/`: `siren-win-kit` → `siren-windows-amd64-kit`, `siren-linux-kit` → `siren-linux-amd64-kit`, новый `siren-linux-arm64-kit` (бинарник + config.example.json + install.sh/uninstall.sh с aarch64 + siren.service + README.txt); zip переименованы, `make-release.ps1` (временный скрипт) обновлён. Проверка: три zip, внутри правильные файлы, `install.sh` из arm64-кита отклоняет x86_64-машину (dry-run проверки условия без установки)

## 2. Доки

- [x] 2.1 `docs/deployment.md` — секция сборки (три цели), таблица комплектов с архитектурами, ARM-установка (тот же `install.sh`, оговорка про отсутствие SMART на типичном ARM-железе); `README.md` quick start — строка про `siren-linux-arm64-kit.zip`; `deploy/README.md`, `deploy/acceptance.md` — новые имена бинарников/китов. Проверка: grep по докам не находит старых имён `siren-win-kit`/`siren-linux-kit.zip` вне истории релиза v0.3, имена соответствуют матрице

## 3. Полный контур

- [x] 3.1 `gofmt`, `go vet ./...`, `go test ./... -count=1` (тесты `cmd/siren` — приём с `.syso`); кросс-сборка `linux/arm64` `go vet`; пересборка `build.ps1`, китов и zip. Проверка: всё зелёное, артефакты обновлены, хэш `siren-linux-arm64` совпадает между bin и китом

## 4. Живая проверка

- [ ] 4.1 Установка из `siren-linux-arm64-kit.zip` на реальном ARM64-железе (Ubuntu/Debian, systemd): `sudo ./install.sh` → служба активна, ловушки/двери/Telegram работают, boot-датчик пишет `data/boot.json` молча. Проверка: вывод journalctl, алерт в Telegram; при отсутствии железа — отметить «не проверено», железо уточнить у владельца
