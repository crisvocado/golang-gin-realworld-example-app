FROM golang:1.21-bookworm

RUN rm -rf /var/lib/apt/lists/* && \
    apt-get -o Acquire::Retries=3 -o Acquire::http::No-Cache=true update && \
    apt-get -o Acquire::Retries=3 --no-install-recommends install -y build-essential && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download
