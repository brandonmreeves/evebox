FROM fedora:25
MAINTAINER Jason Ish <ish@unx.ca>

RUN rpm -Uvh https://evebox.org/files/rpm/stable/evebox-release.noarch.rpm && \
    dnf install --disablerepo=* --enablerepo=evebox -y evebox && \
    /usr/bin/evebox version

EXPOSE 5636

COPY /docker-entrypoint.sh /

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["evebox", "server"]
