# Multi-stage build of the kernel daemon (llmobsd) for the lite profile.
FROM golang:1.24 AS build
WORKDIR /src
COPY kernel/ ./kernel/
WORKDIR /src/kernel
RUN CGO_ENABLED=0 go build -trimpath -o /out/llmobsd ./cmd/llmobsd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/llmobsd /llmobsd
EXPOSE 4317 4318 8080
ENTRYPOINT ["/llmobsd"]
