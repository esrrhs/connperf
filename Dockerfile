FROM golang AS build-env

RUN GO111MODULE=off go get -u github.com/esrrhs/connperf
RUN GO111MODULE=off go get -u github.com/esrrhs/connperf/...
RUN GO111MODULE=off go install github.com/esrrhs/connperf

FROM debian
COPY --from=build-env /go/bin/connperf .
WORKDIR ./
