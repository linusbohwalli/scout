FROM golang
EXPOSE 4222

#RUN mkdir /etc/scout/

ADD pkg/var/log/scout/scout.log /var/log/scout/scout.log
ADD pkg/etc/scout/scout.yaml /etc/scout/scout.yaml
ADD ./bin/scout /usr/bin
CMD ["/usr/bin/scout", "start"]
