TAG :=	jasonish/evebox

all: build

build:
	docker build --rm -t $(TAG) .

build-nocache:
	docker build --no-cache --rm -t $(TAG) .

shell:
	docker run --rm -it $(TAG) sh
