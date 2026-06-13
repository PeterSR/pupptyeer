# pupptyeer

> **Use [`pupptyeer-client`](https://pypi.org/project/pupptyeer-client/) instead.**
> This package is only a thin alias that exists so the bare `pupptyeer` name
> resolves on PyPI. It contains no implementation of its own.

`pip install pupptyeer` simply pulls in [`pupptyeer-client`](https://pypi.org/project/pupptyeer-client/)
and re-exports its public API, so this works:

```python
from pupptyeer import PupptyeerClient   # same class as pupptyeer_client.PupptyeerClient
```

But you should depend on **`pupptyeer-client`** directly:

```sh
pip install pupptyeer-client
```

```python
from pupptyeer_client import PupptyeerClient
```

That is the real, documented Python client for the
[pupptyeer](https://github.com/PeterSR/pupptyeer) daemon (NDJSON over a unix socket, standard library
only). See its [README](https://pypi.org/project/pupptyeer-client/) and the
[protocol spec](https://github.com/PeterSR/pupptyeer/blob/main/PROTOCOL.md) for usage.

## License

MIT
