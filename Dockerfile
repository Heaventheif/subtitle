# بناء ثنائي ثابت (بلا CGO) ثم صورة صغيرة جداً
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subtitle-server .

FROM scratch
COPY --from=build /out/subtitle-server /subtitle-server
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENV PORT=8000
EXPOSE 8000
ENTRYPOINT ["/subtitle-server"]
