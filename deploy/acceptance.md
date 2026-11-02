# Чек-лист приёмки агента siren на целевых машинах

Документ закрывает живые проверки, которые невозможно выполнить на стенде
разработки: реальный Telegram-бот, права администратора, Linux-машина,
перезагрузка и период приработки. Каждый шаг помечен номером задачи из
`openspec/changes/add-security-siren-agent/tasks.md` — после прогона
отметить соответствующие чекбоксы.

Подготовка: собрать бинарники (`.\build.ps1` → `bin\siren-windows-amd64.exe`
и `bin\siren-linux-amd64`), завести тестового Telegram-бота (@BotFather →
токен), узнать `chat_id` тестового чата (добавить бота, написать ему,
открыть `https://api.telegram.org/bot<token>/getUpdates` → `chat.id`).

---

## 1. Windows-машина (профиль desktop)

### 1.1 Установка службы [7.1]

Выполнять в elevated-сессии PowerShell в каталоге агента (рядом `config.json`,
созданном из `config.example.json`; токен и chat_id — реального бота,
`log_level: "debug"`).

```powershell
.\siren-windows-amd64.exe -service install
.\siren-windows-amd64.exe -service start
.\siren-windows-amd64.exe -service status   # → running
Get-Content .\siren.log -Tail 20            # "siren agent started", ловушки запущены
```

Ожидания:
- служба переживает закрытие сессии (процесс не дочерний консоли);
- в журнале нет `subscription failed` для Security (LocalSystem читает
  Security; если использовали `-service-account` — проверьте членство
  в «Event Log Readers»: `net localgroup "Event Log Readers"`).

### 1.2 Ловушки «с себя» [7.1, 8.1]

```powershell
# telnet-ловушка (порт из профиля desktop: 23) и http (80)
$t = New-Object Net.Sockets.TcpClient; $t.Connect("127.0.0.1", 23)
# прочитать баннер: для ftp/telnet/vnc/mysql/postgres/rdp ответ придёт сразу
```

Ожидание: в Telegram приходит «🚨 <машина>: подключение к ловушке» с портом
и эмулятором; повторные подключения за окно агрегации сворачиваются в
дайджест «(+N за <окно>)».

Если установлен nmap — прогнать `nmap -sT -Pn <ip>` по портам профиля и
сверить количество уведомлений (одно первое событие на порт + дайджесты,
не по сообщению на каждый порт).

### 1.3 RDP-факты [3.5, 8.1]

С другой машины (или mstsc на себя): **неудачный** вход (неверный пароль),
затем **успешный**.

Ожидания в Telegram:
- на неудачу: «ПОПЫТКА ВХОДА», IP источника, учётка, порт 3389 (событие 4625,
  LogonType 10);
- на успех: «ВХОД» (событие 4624 типа 10 и/или 1149 из
  RemoteConnectionManager; дубликат 4624+1149 должен быть схлопнут дедупом —
  одно сообщение, а не два);
- hostname источника — из reverse DNS или ручной карты `name_map`.

Проверка прав датчика без RDP: `wevtutil qe Security /q:"*[System[(EventID=4625)]]"
/c:1 /rd:true /f:text` от учётной записи службы (RunAs) — должно читаться.

### 1.4 Перезагрузка [7.3]

```powershell
Restart-Computer
# после подъёма:
Get-Service siren                    # Running (автостарт)
Get-Content .\siren.log -Tail 5      # новый "siren agent started"
# повторить подключение к ловушке — алерт приходит
```

### 1.5 Откат [7.1]

```powershell
.\siren-windows-amd64.exe -service stop
.\siren-windows-amd64.exe -service remove
Get-Service siren   # → не найдена
```

Ожидание: агент не оставил следов вне своего каталога (кроме записи в
журнале служб); каталог агента с сертификатом и логом удаляется вручную.

---

## 2. Linux-машина (профиль server)

### 2.1 Установка юнита [7.2]

```bash
sudo mkdir -p /opt/siren
sudo cp bin/siren-linux-amd64 /opt/siren/siren
sudo cp config.example.json /opt/siren/config.json   # токен бота, chat_id
sudo cp deploy/siren.service /etc/systemd/system/siren.service
sudo systemctl daemon-reload
sudo systemctl enable --now siren
systemctl status siren
```

Ожидания:
- `systemd-analyze verify /etc/systemd/system/siren.service` — без ошибок;
- в `journalctl -u siren` — «siren agent started»;
- процесс работает не от root: `systemctl show siren -p User,AmbientCapabilities`
  → `CAP_NET_BIND_SERVICE`;
- юнит sshd: в конфиге `sshd_unit` = `ssh` (Debian/Ubuntu) или `sshd`
  (RHEL); `systemctl cat ssh` подтверждает имя.

### 2.2 Права на журнал [3.2, 7.2]

```bash
sudo journalctl -u siren -f &          # наблюдение
# под учёткой службы журнал ssh должен читаться:
sudo runuser -u <служебная-учётка> -- journalctl -u ssh -n 1
```

Ожидание: чтение не падает по правам (группа systemd-journal из юнита).

### 2.3 SSH-факты [3.2, 3.5, 8.2]

С другой машины:
- 3–5 неудачных входов: `ssh wronguser@<host>` с неверным паролем
  (или `sshpass -p bad ssh ...`);
- один успешный вход.

Ожидания в Telegram:
- первое «ПОПЫТКА ВХОДА» с IP и учёткой немедленно, дайджест «+N за <окно>»
  по остальным (сценарий брутфорса: 50 попыток → 1 событие + 1 дайджест);
- «ВХОД» на успешный вход с IP и учёткой;
- IP источника — не `::1`/локальный, если вход с другой машины.

### 2.4 Ловушки [8.2]

```bash
nc 127.0.0.1 21        # баннер FTP, затем отказ
nc 127.0.0.1 5900      # RFB-баннер VNC
nc 127.0.0.1 3306      # greeting MySQL
```

Ожидание: алерты подключений с портом и эмулятором; занятые системные
порты (sshd 22, если работает) пропускаются с WARN в журнале агента, а не
валят старт.

### 2.5 Перезагрузка [7.3]

```bash
sudo reboot
# после подъёма:
systemctl status siren          # active (running), автостарт сработал
journalctl -u siren -b -n 5
```

---

## 3. Реальный Telegram [6.3, 8.1]

- [ ] первое событие приходит сразу после подключения к ловушке;
- [ ] дайджест приходит через <окно агрегации>+~1с с числом повторов;
- [ ] формат читаем: машина, время, тип, порт/протокол, IP/hostname/MAC,
      учётка для входов; «имя не определено» для источников без PTR;
- [ ] длинный брутфорс не валит бота: сообщения не чаще ~1/сек (лимиты
      API), дайджесты суммируют остальное.

## 4. Нагрузочный сценарий [8.4]

На Windows (или Linux) запустить массовый скан/брутфорс:

```powershell
# 500 подключений к одной ловушке
1..500 | ForEach-Object { $c = New-Object Net.Sockets.TcpClient;
  $c.Connect("127.0.0.1", 23); $c.Close() }
```

Ожидания:
- в Telegram: 1 первое событие + дайджесты с суммарным количеством,
  равным числу подключений (по журналу агента сверить `count` дайджестов);
- в `siren.log` нет `queue full`/`messages lost`;
- память процесса стабильна (диспетчер задач / `ps -o rss`), нет роста
  после закрытия окон;
- при искусственном 429 (ограничить лимиты бота или прокси) агент
  повторяет доставку и догоняет очередь — в логе `backing off`, затем
  доставлено всё.

## 5. Период приработки [8.3]

Сутки-двое работы на целевых машинах в доверенной сети:
- собрать из журнала агента все источники алертов;
- легитимные сканеры (антивирус, мониторинг, резервное копирование) —
  внести IP/CIDR в `allowlist` конфига и перезапустить службу/юнит;
- проверить: после перезапуска алертов от них нет, чужие источники
  алертятся как прежде.

## 6. Откат (Linux) [7.2]

```bash
sudo systemctl disable --now siren
sudo rm /etc/systemd/system/siren.service /opt/siren -r
sudo systemctl daemon-reload
```

---

## Сводка: какие задачи закрываются где

| Шаг | Задачи |
|---|---|
| 1.1, 1.5 | 7.1 |
| 1.2 | 8.1 (Windows), 7.1 |
| 1.3 | 3.5 (Windows), 8.1 |
| 1.4, 2.5 | 7.3 |
| 2.1, 2.2, 6 | 7.2 |
| 2.3 | 3.2, 3.5 (Linux), 8.2 |
| 2.4 | 8.2 |
| 3 | 6.3, 8.1 |
| 4 | 8.4 |
| 5 | 8.3 |
