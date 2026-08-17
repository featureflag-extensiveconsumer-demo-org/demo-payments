# demo-payments synthetic evaluator (Go)

Set `LD_EVALUATION_SDK_KEY` and `DEMO_ENVIRONMENT`, then `go run . --profile staging`. Each batch opens a client, evaluates every flag the release owns, flushes and closes.
