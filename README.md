# respawn

`respawn` は、指定したコマンドを起動し、終了したら再起動する小さな Go 製ラッパーです。
子プロセスの stdout/stderr を監視し、指定した正規表現に一致する行が出た場合も子プロセスを停止して再起動します。

デフォルトでは以下のコマンドを監視します。

```bash
codex remote-control
```

デフォルトの再起動トリガーは以下の正規表現です。

```text
write_stdin failed: stdin is closed for this session
ERROR
```

## Requirements

- Go 1.22 以上
- Linux / Unix 系環境
  - プロセスグループに signal を送って子プロセスを停止します
- デフォルト設定で使う場合は `codex` コマンドが PATH 上にあること

確認例:

```bash
go version
which codex
```

## Build

```bash
go build -o respawn .
```

必要に応じて PATH の通ったディレクトリへ配置してください。

## Usage

引数なしで実行すると `codex remote-control` を監視します。

```bash
respawn
```

これは以下と同等です。

```bash
respawn -- codex remote-control
```

任意のコマンドを監視する例:

```bash
respawn -- your-command arg1 arg2
```

再起動前の待機時間を変える例:

```bash
respawn --delay 5s
```

再起動トリガーを変更する例:

```bash
respawn --restart-on 'stdin is closed' -- codex remote-control
```

## Options

```text
Usage:
  respawn [options] [-- command args...]

Default command is: codex remote-control

Options:
  --restart-on REGEX   restart when stdout/stderr line matches REGEX
                       can be specified multiple times
  --delay DURATION     wait before restarting (default: 2s)
  --max-restarts N     stop after N restarts; 0 means unlimited (default: 0)
  --lock-file PATH     prevent concurrent respawn instances with this lock file
                       default for codex remote-control: /tmp/respawn-codex-remote-control.lock
```

## Behavior

- 子プロセスの stdout/stderr を親プロセスの stdout/stderr にそのまま流します。
- `--restart-on` に指定した正規表現に一致する行を検出すると、子プロセスグループへ `SIGTERM` を送って再起動します。
- `SIGTERM` 後、10 秒以内に終了しない場合は子プロセスグループへ `SIGKILL` を送ります。
- 子プロセスが終了した場合は、終了コードにかかわらず再起動します。
- `Ctrl-C` / `SIGTERM` / `SIGHUP` を受け取ると、子プロセスグループへ signal を転送して `respawn` も終了します。
- `--max-restarts` に 1 以上を指定すると、その回数の再起動後に終了します。
- `codex remote-control` を監視する場合、既存の `codex remote-control` プロセスがあれば起動せず、デフォルトで `/tmp/respawn-codex-remote-control.lock` も使って二重起動を防ぎます。

## Examples

無制限に再起動:

```bash
respawn
```

最大 10 回だけ再起動:

```bash
respawn --max-restarts 10
```

複数のエラーパターンで再起動:

```bash
respawn \
  --restart-on 'write_stdin failed: stdin is closed' \
  --restart-on 'ERROR' \
  --restart-on 'Full-history forked agents inherit' \
  -- codex remote-control
```
