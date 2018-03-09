gen-rpc:
	protoc -I ./api ./api/scout.proto --go_out=plugins=grpc:./api

remove-all:
	rm ./api/scout.pb.go
