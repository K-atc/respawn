# codex-remote-control-respawn

`codex remote-control` が特定のエラーを出して機能停止したときに、自動でプロセスを終了して再起動するための小さな Go 製ラッパーです。

デフォルトでは以下のコマンドを起動します。

```bash
codex remote-control
```

標準出力または標準エラー出力に以下の文字列が出たら再起動します。

```text
write_stdin failed: stdin is closed for this session
```

## Requirements

- Go 1.22 以上
- `codex` コマンドが PATH 上にあること
- Linux / Unix 系環境
  - プロセスグループに signal を送って子プロセスを停止します

確認例:

```bash
go version
which codex
```

## Build

このディレクトリでビルドします。

```bash
go build -o respawn .
```

任意のディレクトリに配置する例:

```bash
go build -o ./codex-remote-control-respawn .
```

## Usage

通常は引数なしで実行します。

```bash
codex-remote-control-respawn
```

これは以下と同等です。

```bash
codex-remote-control-respawn -- codex remote-control
```

再起動前の待機時間を変える例:

```bash
codex-remote-control-respawn --delay 5s
```

再起動トリガーを追加または変更する例:

```bash
codex-remote-control-respawn --restart-on 'stdin is closed' -- codex remote-control
```

任意のコマンドを監視する例:

```bash
codex-remote-control-respawn --restart-on 'fatal error' -- your-command arg1 arg2
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
```

## Behavior

- 子プロセスの stdout/stderr をそのまま親の stdout/stderr に流します。
- 指定した正規表現に一致する行を検出すると、子プロセスグループへ `SIGTERM` を送ります。
- 10 秒以内に終了しない場合は `SIGKILL` を送ります。
- 子プロセスが通常終了した場合も再起動します。
- `Ctrl-C` / `SIGTERM` / `SIGHUP` は子プロセスグループへ転送して終了します。

## Examples

無制限に再起動:

```bash
codex-remote-control-respawn
```

最大 10 回だけ再起動:

```bash
codex-remote-control-respawn --max-restarts 10
```

複数のエラーパターンで再起動:

```bash
codex-remote-control-respawn \
  --restart-on 'write_stdin failed: stdin is closed' \
  --restart-on 'Full-history forked agents inherit' \
  -- codex remote-control
```
