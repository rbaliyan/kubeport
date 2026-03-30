FROM gcr.io/oss-fuzz-base/base-builder-go@sha256:141af7491cb3e98ab08f8184d0780856b1f7f54d77cd06637a5401941269ccf3

COPY . $SRC/kubeport
COPY .clusterfuzzlite/build.sh $SRC/build.sh
WORKDIR $SRC/kubeport
