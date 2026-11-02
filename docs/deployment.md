# Установка и эксплуатация

Два пути: готовый комплект под платформу (рекомендуется) или сборка из
исходников. Все команды проверены на Windows 10/11 x64 и Linux amd64
(systemd).

## Сборка из исходников

Нужен Go 1.2x. На Windows:

```powershell
.\build.ps1 -Version 0.3.0
```

В `bin/` появятся `siren-windows-amd64.exe` и `siren-linux-amd64`
(статический). Состав комплекта под каждую платформу описан ниже — соберите
каталог из бинарника, `config.example.json`, установочного скрипта и
(для Linux) юнита `siren.service` из `deploy/`.

# Windows 10/11 (x64, служба + пульт)

## Состав комплекта

| Файл | Что это |
|---|---|
| `siren-windows-amd64.exe` | агент: служба + пульт в трее, один бинарник |
| `config.example.json` | шаблон конфига (профиль desktop) |
| `install.ps1` | установка (elevated PowerShell) |
| `uninstall.ps1` | удаление, `-Purge` — начисто |

## Установка

```powershell
# PowerShell от имени администратора, каталог комплекта:
.\install.ps1
```

Установка копирует агента в `%ProgramFiles%\siren\siren.exe`, создаёт
`config.json` из шаблона (если его ещё нет), ставит службу `siren`
(автостарт, LocalSystem — иначе не прочитать Security-журнал) и открывает
правило брандмауэра «siren traps».

Проверка:

```powershell
Get-Service siren                                   # -> Running
Get-Content "$env:ProgramFiles\siren\siren.log" -Tail 20
```

Ожидаются строки `siren agent started` (с полем `ip=`), `trap listeners
started`, `doors: subscribed channel Security`. Предупреждение `trap port
not opened, skipping` — норма: порт занят реальным сервисом, за ним следит
датчик входов.

## Пульт управления (трей)

Двойной клик по `siren.exe` → UAC → иконка в трее и окно управления:

- статус службы, кнопки «Установить и запустить» / «Стоп» / «Удалить»;
- галочки ловушек (вкл/выкл), добавление/удаление своих портов;
- настройки (имя, token, chat_id, окно агрегации, allowlist, лог) с кнопкой
  «Сохранить и перезапустить службу» — с валидацией, при ошибке ничего
  не записывается.

Повторный запуск просто поднимает окно первого экземпляра. Пока служба
работает, пульт не запускает второй конвейер. Закрытие окна сворачивает его
в трей.

Отладка в терминале: `.\siren.exe -console` — запускать при **остановленной**
службе, иначе конвейер пропустит занятые порты и задвоит датчики дверей.

## Обновление и удаление

```powershell
# Обновление: заменить бинарник в комплекте и повторить
.\install.ps1

# Удаление
.\uninstall.ps1          # служба и правило брандмауэра; конфиг/логи остаются
.\uninstall.ps1 -Purge   # удалить и каталог %ProgramFiles%\siren
```

# Linux (amd64 + systemd)

## Состав комплекта

| Файл | Что это |
|---|---|
| `siren` | бинарник агента (статический) |
| `config.example.json` | шаблон конфига (профиль server, `sshd_unit=ssh`) |
| `siren.service` | systemd-юнит (журнал + CAP_NET_BIND_SERVICE) |
| `install.sh` | установка, ставит smartmontools и nvme-cli |
| `uninstall.sh` | удаление |

## Установка

```bash
sudo ./install.sh
# первый запуск создаёт /opt/siren/config.json из шаблона и НЕ стартует
# службу, пока в конфиге стоит токен-плейсхолдер

sudo nano /opt/siren/config.json   # token, chat_id, agent_name, sshd_unit

sudo systemctl enable --now siren
systemctl status siren
journalctl -u siren -f
```

Ожидаются строки `siren agent started` (с полем `ip=`), `trap listeners
started`, `doors: started journalctl reader`. Конфиг — `0600 root`
(там токен), править через `sudo`.

## Обновление и удаление

```bash
# Обновление: заменить файл siren в комплекте на новый и повторить
sudo ./install.sh          # бинарник и юнит обновятся, служба перезапустится

# Удаление
sudo ./uninstall.sh        # конфиг и журнал остаются в /opt/siren
sudo rm -rf /opt/siren     # удалить всё начисто
```

# Проверка работы

Со своей машины (или с самой машины — добавьте `127.0.0.1` и `::1` в
allowlist, чтобы тесты не алертили):

```bash
# Linux
nc 127.0.0.1 21          # баннер FTP-ловушки -> алерт «подключение к ловушке»
nc 127.0.0.1 5900        # баннер VNC
ssh wronguser@localhost -o PreferredAuthentications=password \
    -o PubkeyAuthentication=no      # любой пароль -> «ПОПЫТКА ВХОДА»
```

```powershell
# Windows
Test-NetConnection 127.0.0.1 -Port 23       # telnet-ловушка -> алерт
Test-NetConnection 127.0.0.1 -Port 3306     # MySQL-ловушка
ssh wronguser@127.0.0.1 -o PreferredAuthentications=password `
    -o PubkeyAuthentication=no              # «ПОПЫТКА ВХОДА» с учёткой
```

Успешный вход с другой машины по SSH/RDP → алерт «ВХОД»; неудачный →
«ПОПЫТКА ВХОДА».

## Как выглядят алерты (текстовые примеры)

```
🚨 SRV-01 (192.0.2.10): подключение к ловушке
время: 2026-11-02 17:58:52
источник: 203.0.113.44 · имя не определено
порт: 5900 (vnc)
```

```
🚨 SRV-01 (192.0.2.10): ПОПЫТКА ВХОДА
время: 2026-11-02 03:14:07
источник: 198.51.100.23 · имя не определено
протокол: ssh · порт: 22
учётка: root
```

```
🚨 WIN-PC01 (192.0.2.26): ТРЕВОГА ДИСКОВ
время: 2026-11-02 09:00:03
источник: IP неизвестен
диск sdb: статус ok -> warn (reallocated 0 -> 8)
```

Всплеск сканирования приходит одним алертом со счётчиком событий, а не
простынёй.

# Эксплуатация

## Здоровье дисков вне расписания

```bash
# Linux — ничего не пишет в state и не отправляет в Telegram:
sudo /opt/siren/siren -health-report
```

На Windows то же делает секция «Диски (сторож здоровья)» в пульте:
«Обновить» перечитывает снимок службы, «Проверить сейчас» собирает свежие
данные только в памяти пульта.

Расписание и пороги — секция `health` в конфиге
(см. [configuration.md](configuration.md)).

## Где что лежит

| Платформа | Конфиг | Журнал | Состояние |
|---|---|---|---|
| Windows | `%ProgramFiles%\siren\config.json` | `%ProgramFiles%\siren\siren.log` | `%ProgramFiles%\siren\data\` |
| Linux | `/opt/siren/config.json` | `/opt/siren/siren.log` (systemd-строки — в journald) | `/opt/siren/data/` |

После рестарта службы агент сравнивает свежий снимок здоровья с сохранённым
и сообщает об изменениях, случившихся за простой; «всё хорошо» при старте
не отправляется.

## Приёмка

Полные чек-листы проверки установки — [`deploy/acceptance.md`](../deploy/acceptance.md):
раздел 1 — Windows, раздел 2 — Linux.
