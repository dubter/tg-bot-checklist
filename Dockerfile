FROM golang:1.24 as builder

WORKDIR /app

COPY . .

RUN go mod tidy && go build -o checklist

FROM alpine:latest

WORKDIR /app
COPY --from=builder /app/checklist .

CMD ["./checklist"]