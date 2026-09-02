FROM golang:1.24.1-alpine AS builder

RUN apk update && apk upgrade && apk --no-cache add ca-certificates

COPY . /go/src/github.com/Halo1211/discord-downloader-go-gui
WORKDIR /go/src/github.com/Halo1211/discord-downloader-go-gui

RUN go mod download
RUN CGO_ENABLED=0 GODEBUG=http2client=0 GOOS=linux GOARCH=amd64 go build -a -o app .

FROM scratch
WORKDIR /root/
COPY --from=builder /go/src/github.com/Halo1211/discord-downloader-go-gui/app .
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["./app"]
