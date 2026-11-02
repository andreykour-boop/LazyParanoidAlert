# Tasks

## 1. Чистота репозитория

- [x] 1.1 `.gitignore`: игнор `agent/`, `bin/`, `dist/` (целиком, D1), точечно `*.log`, `*.exe`, `*.exe~`, `*.syso`, `opencode.json`, `AGENTS.md`, `.opencode/`. Плюс `.gitattributes` (LF для sh/service, CRLF для ps1/bat — иначе CRLF-чекаут сломал бы Linux-скрипты). Проверка: после `git init -b main` и `git add .` в индексе нет ни одного файла из игнорируемых путей (`git status --ignored` показывает agent/, bin/, dist/, opencode.json, AGENTS.md, .opencode/, *.syso в секции ignored), staged = 160 файлов
- [x] 1.2 Тестовые фикстуры — `192.168.11.26` → `192.0.2.26`, `192.168.35.10` → `198.51.100.10` (диапазон совпадал с реальной локальной сетью), `DESKTOP-4FCDLB0` → `TEST-PC` (format_disk_test.go, format_test.go, doors_test.go; RFC 5737). Проверка: `go test ./internal/notify/ ./internal/doors/ -count=1` зелёный; grep по `cmd/`, `internal/` не находит `192.168.11.`, `192.168.35.`, `DESKTOP-4FCDLB0` (массовые учебные `192.168.1.x` остаются сознательно)

## 2. Лицензия и README

- [x] 2.1 `LICENSE` — MIT, «Copyright (c) 2026 Andrey». Проверка: файл создан, текст — стандартный шаблон MIT
- [x] 2.2 `README.md` — что это, ASCII-схема конвейера, «что умеет» (ловушки, двери, allowlist, агрегация, сторож), быстрый старт (сборка, киты, nc-проверка), таблица-указатель на docs/, разработка, лицензия. Проверка: ссылки README ведут на создаваемые в группе 3 файлы docs/; токены/IP — только плейсхолдеры и учебные адреса

## 3. Документация docs/

- [x] 3.1 `docs/architecture.md` — ASCII-схема конвейера, разделы по ловушкам, дверям (в т.ч. штамповка сервисных портов), allowlist-семантике, агрегации/доставке, сторожу здоровья, пульту. Проверка: разделы соответствуют 5 main-спекам, ссылка на openspec/specs/ живая
- [x] 3.2 `docs/configuration.md` — пример конфига + справочник всех параметров: `agent_name`, `profile` (с полным списком портов профилей из `defaultTraps`), `traps` (emulator-имена, tls), `telegram` (token, chat_id, api_url), `aggregate_window_seconds`, `allowlist` (таблица форматов с портами, правила: порт 0 не гасится, фиксированные 22/3389, нестандартный sshd), `sshd_unit`, `name_map`, `data_dir`, `log_level`, `log_file`, `health` (все 5 полей с диапазонами валидатора). Проверка: каждому полю из `config.example.json` и структуры Config найдётся раздел; ничего сверх валидатора
- [x] 3.3 `docs/deployment.md` — сборка из исходников, Windows (состав кита, install.ps1, пульт, -console, обновление/удаление), Linux (install.sh, systemd, обновление/удаление), проверка ловушек nc/Test-NetConnection, три текстовых примера алертов на вымышленных адресах (RFC 5737/198.51.100), эксплуатация `-health-report`, таблица путей, ссылка на acceptance. Проверка: команды дословно совпадают с README.txt китов; ссылка на ../dist убрана (dist не входит в репо), локальные ссылки проверены Test-Path

## 4. Git и публикация

- [x] 4.1 `git init -b main`; `git add .`; контроль перед коммитом: staged = 165 файлов (internal 85, openspec 50, cmd 15, docs 3, deploy 3 + корень), игнорируемых нет; grep по staged на паттерны токена — только плейсхолдер `123456:REPLACE-WITH-BOT-TOKEN` (специальная проверка реального токена win-кита — ноль); коммит `ab2bf34`
- [x] 4.2 Push на `https://github.com/kur890440-ops/siren` — выполнен (`* [new branch] main -> main`, remote origin добавлен, ветка отслеживает origin/main)

## 5. Финальная сверка

- [x] 5.1 Полный контур: `gofmt` clean, `go vet ./...` 0, `go test ./... -count=1` — все пакеты ok (тесты cmd/siren — приём с `.syso`, возвращён на место). Сборка не требовалась: продукта код не менялся, только тесты
- [ ] 5.2 Контрольная вычитка репо глазами «внешнего посетителя»: README читабелен без знания проекта, из docs/ можно поставить агент и найти любой параметр конфига. Проверка: владелец подтвердил (или владелец открыл страницу GitHub и подтвердил вид)

## Workflow follow-up

- Заархивировать change после ревью (`/opsx-archive`).
- Напомнить: Windows-агент обновить через `run_ui_batch.ps1` (отдельно от этого change).
