gen-rpc:
	protoc -I ./api ./api/fileEventEmitter.proto --go_out=plugins=grpc:./api
