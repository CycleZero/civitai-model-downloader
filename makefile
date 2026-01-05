
BIN_NAME=cvtcli

build:
	go build -o ./bin/cvtcli ./


build-windows:
	go build -o ./bin/cvtcli.exe ./
