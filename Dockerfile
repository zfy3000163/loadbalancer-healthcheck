# Build the manager binary
FROM harbor.lenovo.com/base/golang:1.19.5-buster as dev-go

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
#RUN go mod download

# Copy the go source
COPY main.go main.go
COPY apis/ apis/
COPY controllers/ controllers/
COPY client/ client/

# Build
ENV GO111MODULE=on
ENV GOPROXY=http://pip.lenovo.com/repository/go-group/
ENV GOSUMDB=off
RUN CGO_ENABLED=0 GOOS=linux go build -a -o lbhc-controller main.go


FROM harbor.lenovo.com/base/centos:7.9.2009 as dev 

WORKDIR /workspace

# Copy the go source
COPY tengine-master nginx 

# Build
RUN sed -e 's|^mirrorlist=|#mirrorlist=|g' \
        -e 's|^#baseurl=http://mirror.centos.org/\(.\{1,\}\)/$releasever|baseurl=http://pip.lenovo.com/repository/centos-proxy/$releasever|g' \
        -i.bak /etc/yum.repos.d/CentOS-Base.repo && \
    yum -y install bash make gcc automake pcre.x86_64 pcre-devel.x86_64 zlib.x86_64 zlib-devel.x86_64 && \
    yum -y install openssl-devel libxml2-devel && yum clean all

WORKDIR /workspace/nginx

#RUN ./auto/configure --prefix=/usr/local/nginx --add-module=ngx_healthcheck_module --with-stream && make && make install
RUN ./configure --prefix=/usr/local/nginx --add-module=ngx_healthcheck_module --with-stream && make && make install

RUN mkdir -p /usr/local/nginx/conf/conf.d && mkdir -p /usr/local/nginx/conf/conf.d/stream && \
    touch /usr/local/nginx/conf/conf.d/upstream.conf && touch /usr/local/nginx/conf/conf.d/stream/upstream.conf

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM harbor.lenovo.com/base/centos:7.9.2009

RUN sed -e 's|^mirrorlist=|#mirrorlist=|g' \
        -e 's|^#baseurl=http://mirror.centos.org/\(.\{1,\}\)/$releasever|baseurl=http://pip.lenovo.com/repository/centos-proxy/$releasever|g' \
        -i.bak /etc/yum.repos.d/CentOS-Base.repo && \
    yum -y install bash make gcc automake pcre.x86_64 pcre-devel.x86_64 zlib.x86_64 zlib-devel.x86_64 && \
    yum -y install openssl-devel libxml2-devel && yum clean all

WORKDIR /
COPY --from=dev /usr/local/nginx /usr/local/nginx
COPY --from=dev-go /workspace/lbhc-controller .

RUN groupadd -r nginx && useradd -r -g nginx nginx 
RUN chown nginx:nginx /usr/local/ -R

USER nginx:nginx

ENTRYPOINT ["/lbhc-controller"]

