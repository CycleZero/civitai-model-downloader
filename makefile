
BIN_NAME=cvtcli

build:
	go build -o ./bin/cvtcli ./


build-windows:
	go build -o ./bin/cvtcli.exe ./

build-linux:
	GOOS=linux GOARCH=amd64 go build -o ./bin/cvtcli ./