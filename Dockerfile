FROM golang:1.24 as builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o checklist main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/checklist .

RUN ls -l /app

CMD ["./checklist"]