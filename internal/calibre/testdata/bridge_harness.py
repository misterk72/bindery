"""Serve the real calibre-bridge request handler with no Calibre installed.

Used by TestPluginContract in the Go suite so the Go client is exercised
against the Python code that will actually answer it, rather than against a
hand written httptest fake that can drift from it.

The mechanism is the plugin's own conftest.py: stub `calibre` and `qt` in
sys.modules before the plugin package is imported, then register the plugin
checkout under the `calibre_plugins.bindery_bridge` name Calibre would give
it. Nothing in the plugin is modified or monkeypatched, so a protocol change
on either side shows up as a failing assertion rather than as drift.

Usage: bridge_harness.py <plugin-root> [--api-key K] [--max-body N]
                                       [--unavailable N]
Prints "PORT <n>" on stdout once it is listening.
"""

import argparse
import re
import sys
import types


def stub_calibre_and_qt():
    def module(name):
        mod = types.ModuleType(name)
        sys.modules[name] = mod
        return mod

    module("calibre")
    customize = module("calibre.customize")
    customize.InterfaceActionBase = object

    module("calibre.utils")
    utils_config = module("calibre.utils.config")

    class JSONConfig(dict):
        def __init__(self, name):
            super().__init__()
            self.defaults = {}

        def get(self, key, default=None):
            return super().get(key, self.defaults.get(key, default))

    utils_config.JSONConfig = JSONConfig

    utils_date = module("calibre.utils.date")
    utils_date.parse_date = lambda value: value

    module("calibre.gui2")
    gui2_actions = module("calibre.gui2.actions")
    gui2_actions.InterfaceAction = object

    module("calibre.ebooks")
    module("calibre.ebooks.metadata")
    meta = module("calibre.ebooks.metadata.meta")

    class Metadata:
        def __init__(self):
            self.title = "Stub"
            self.authors = []
            self.identifiers = {}
            self.rating = None
            self.series = None
            self.series_index = None
            self.cover_data = None

        def set_identifiers(self, ids):
            self.identifiers = dict(ids)

    meta.get_metadata = lambda stream, fmt: Metadata()

    constants = module("calibre.constants")
    constants.numeric_version = (9, 8, 0)

    qt = module("qt")
    qt_core = module("qt.core")
    for name in ("QDialog", "QDialogButtonBox", "QVBoxLayout", "QFormLayout",
                 "QLineEdit", "QSpinBox", "QWidget", "QPushButton", "QTimer"):
        setattr(qt_core, name, object)
    qt.core = qt_core


def register_plugin_package(root):
    pkg = types.ModuleType("calibre_plugins")
    pkg.__path__ = []
    sys.modules["calibre_plugins"] = pkg
    bridge = types.ModuleType("calibre_plugins.bindery_bridge")
    bridge.__path__ = [root]
    sys.modules["calibre_plugins.bindery_bridge"] = bridge
    pkg.bindery_bridge = bridge


IDENTIFIER_QUERY = re.compile(r'^identifiers:"?=([^:"]+)"?:"?=(.+?)"?$')


class FakeNewAPI:
    """The slice of Calibre's db.new_api that adder.py touches."""

    def __init__(self):
        self._next_id = 1
        self._by_identifier = {}
        self._books = {}

    def add_books(self, entries, add_duplicates=False, run_hooks=True):
        ids = []
        for mi, _formats in entries:
            book_id = self._next_id
            self._next_id += 1
            for key, value in (getattr(mi, "identifiers", None) or {}).items():
                self._by_identifier[(key, value)] = book_id
            self._books[book_id] = mi
            ids.append(book_id)
        return ids, []

    def search(self, query):
        match = IDENTIFIER_QUERY.match(query)
        if not match:
            return set()
        found = self._by_identifier.get((match.group(1), match.group(2)))
        return {found} if found else set()

    def find_identical_books(self, mi):
        return set()

    def all_book_ids(self):
        return set(self._books)

    def get_metadata(self, book_id):
        return self._books[book_id]

    def set_metadata(self, book_id, mi):
        self._books[book_id] = mi


class FakeDB:
    def __init__(self, library_path):
        self.library_path = library_path
        self.new_api = FakeNewAPI()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("root")
    parser.add_argument("--api-key", default="")
    parser.add_argument("--max-body", type=int, default=64 * 1024 * 1024)
    parser.add_argument("--unavailable", type=int, default=0)
    parser.add_argument("--library", default="/calibre-library")
    args = parser.parse_args()

    stub_calibre_and_qt()
    register_plugin_package(args.root)

    from calibre_plugins.bindery_bridge.plugin.handlers import make_handler

    db = FakeDB(args.library)
    remaining = {"unavailable": args.unavailable}

    def get_db():
        if remaining["unavailable"] > 0:
            remaining["unavailable"] -= 1
            return None
        return db

    handler = make_handler(
        api_key=args.api_key,
        get_db=get_db,
        get_gui=None,
        ingest_root="",
        max_body_bytes=args.max_body,
    )

    from http.server import ThreadingHTTPServer

    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    sys.stdout.write("PORT %d\n" % server.server_address[1])
    sys.stdout.flush()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    # The plugin logs through the logging module, which writes to stderr, so
    # stdout carries only the port line the Go side parses.
    main()
