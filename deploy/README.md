# Развёртывание агента siren

## Windows (служба)

Сборка: `.\build.ps1` → `bin\siren-windows-amd64.exe`.

```powershell
# от имени администратора, в каталоге агента (рядом config.json)
.\siren-windows-amd64.exe -service install
.\siren-windows-amd64.exe -service start
.\siren-windows-amd64.exe -service status
.\siren-windows-amd64.exe -service stop
.\siren-windows-amd64.exe -service remove
```

- По умолчанию служба работает от `LocalSystem` — он читает Security-журнал
  (события 4624/4625, 1149).
- Выделенная учётная запись: `-service install -service-account DOMAIN\siren
  -service-password ...`; установщик добавит её в группу **Event Log Readers**
  (минимально достаточное право чтения Security). Если добавление не удалось
  (например, нет связи с контроллером домена) — добавьте вручную:
  `net localgroup "Event Log Readers" DOMAIN\siren /add`.
- Служба ставится с автостартом и зависимостью от службы журнала событий;
  относительные пути в конфиге (`data_dir`, `log_file`) разрешаются от
  каталога бинарника.
- Установка службы создаёт программное правило брандмауэра «siren traps»
  (входящий TCP к бинарнику); удаление службы снимает правило. Без правила
  ловушки доступны только с самой машины.
- Удаление службы останавливает её перед `Delete()`.

## Linux (systemd)

Сборка: `make linux` (или `.\build.ps1`) → `bin/siren-linux-amd64` и
`bin/siren-linux-arm64`.

```bash
sudo mkdir -p /opt/siren
sudo cp bin/siren-linux-amd64 /opt/siren/siren
sudo cp config.example.json /opt/siren/config.json   # отредактировать
sudo cp deploy/siren.service /etc/systemd/system/siren.service
sudo systemctl daemon-reload
sudo systemctl enable --now siren
systemctl status siren
```

- Юнит выдаёт процессу группу `systemd-journal` (чтение журнала sshd для
  датчика входов) и `CAP_NET_BIND_SERVICE` (ловушки на портах < 1024).
  Полные root-права не нужны.
- В конфиге задайте `sshd_unit`: `ssh` (Debian/Ubuntu) или `sshd` (RHEL).
- `telegram.api_url` (опционально) переопределяет эндпоинт Bot API — для
  интеграционных стендов и тестов; в продакшене не задавать.
- Проверка прав до запуска агента:
  `sudo -u nobody journalctl -f -u ssh -n 0` не должен падать по правам
  под учёткой службы (или `systemd-analyze verify /etc/systemd/system/siren.service`).

## Сборка комплектов (dist)

Канонические исходники кита живут в репо: [`install.sh`](install.sh),
[`uninstall.sh`](uninstall.sh), [`siren.service`](siren.service) — здесь;
`config.example.json` — в корне. Каталоги и zip в `dist/` — сборочные
артефакты (gitignored), имена несут архитектуру явно:

| Кит | Бинарник | `EXPECTED_ARCH` |
|---|---|---|
| `siren-windows-amd64-kit` | `siren-windows-amd64.exe` | — |
| `siren-linux-amd64-kit` | `siren-linux-amd64` | `x86_64` |
| `siren-linux-arm64-kit` | `siren-linux-arm64` | `aarch64` |

Сборка Linux-кита: создать каталог `dist/<имя-кита>`, скопировать туда
бинарник, `config.example.json`, `siren.service`, `install.sh`,
`uninstall.sh` и `README.txt`; в копии `install.sh` выставить
`EXPECTED_ARCH` под архитектуру кита (шапка файла объясняет проверку).
Ограничение не даёт установить кит на машину с другой архитектурой — ядро
всё равно не смогло бы исполнить бинарник.
