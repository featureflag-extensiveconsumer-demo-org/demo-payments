FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN go mod tidy && CGO_ENABLED=0 go build -o /out/evaluator .

FROM alpine:3.20
RUN adduser -D -u 10001 evaluator
COPY --from=build /out/evaluator /app/evaluator
USER evaluator
ENTRYPOINT ["/app/evaluator"]
