FROM golang:1.23-alpine AS build

WORKDIR /src
COPY . .
RUN go build -trimpath -ldflags='-s -w' -o /out/projector ./services/projector

FROM alpine:3.21
RUN addgroup -S projector && adduser -S -G projector projector
COPY --from=build /out/projector /usr/local/bin/projector
USER projector
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/projector"]
