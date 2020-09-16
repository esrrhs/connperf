FROM golang AS build-env

RUN go get -u github.com/esrrhs/connperf
RUN go get -u github.com/esrrhs/connperf/...
RUN go install github.com/esrrhs/connperf

FROM debian
COPY --from=build-env /go/bin/connperf .
WORKDIR ./
