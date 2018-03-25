# Godocdash

Generate Dash docset for Dash/Zeal from your local $GOPATH packages.

## Features

+ Support `Package`, `Type`, `Function`, `Constant`, `Variable` entry types of dash docsets currently.

+ You can set your own custom docset name and icon for different `$GOPATH`.

+ Concurrent generating, usally it only takes a few seconds to complete.

+ Go standard libraries are ignored, as there's `Go` docset in Dash/Zeal downloads already.

## How It Works

While running, `godocdash` will first start a temporary `godoc` server, then find the package entries to grab the godoc pages, and generate the docset.

## Installing

```
go get github.com/wuudjac/godocdash
```

And make sure `godoc` command is in your `$PATH`.

## Usage

Generally, just run:

```
godocdash
```

And a docset named *GoDoc.docset* will be generated in your current directory, you can then place it into Dash/Zeal docsets path.

As `godocdash` directly passes your current environment variables to `godoc`, you can change the source `$GOPATH` by setting it while running `godocdash`:

```
GOPATH=/another/gopath godocdash
```

You can also change the docset name and icon, or mute the output:

```
GOPATH=/another/gopath godocdash -icon 'new_icon.png' -name 'different name' -silent
```

Command line flags:

```
$ godocdash -h
Usage of godocdash:
  -icon string
    	Docset icon .png path
  -name string
    	Set docset name (default "GoDoc")
  -silent
    	Silent mode (only print error)
```
