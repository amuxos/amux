import copy
import json
import tempfile
import unittest
from pathlib import Path

import docs_catalog as docs


class CatalogTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.data = {'version': 1, 'groups': [{'id': 'start', 'title': {'en': 'Start', 'zh-CN': '开始'}, 'pages': [
            {'source': path, 'title': {'en': path, 'zh-CN': path}} for path in sorted(docs.ROOT_PAGES)
        ]}]}
        for source in docs.ROOT_PAGES | {'docs/README.md'}:
            for lang in docs.LANGS:
                path = self.root / docs.localized(source, lang)
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(f'[English]({Path(source).name}) | [简体中文]({Path(docs.localized(source,"zh-CN")).name})\n')
        self.write_catalog(self.data)

    def write_catalog(self, data):
        (self.root / 'docs/navigation.json').write_text(json.dumps(data))

    def test_missing_translation_fails(self):
        (self.root / 'CONTEXT.zh-CN.md').unlink()
        with self.assertRaisesRegex(ValueError, 'missing translation'):
            docs.load_catalog(self.root)

    def test_unlisted_page_and_duplicate_fail(self):
        (self.root / 'docs/new.md').write_text('# Forgotten page')
        with self.assertRaisesRegex(ValueError, 'unlisted'):
            docs.load_catalog(self.root)
        (self.root / 'docs/new.md').unlink()
        data = copy.deepcopy(self.data)
        data['groups'][0]['pages'] *= 2
        self.write_catalog(data)
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            docs.load_catalog(self.root)

    def test_navigation_escaping_and_historical_status(self):
        data = docs.load_catalog(self.root)
        data['groups'][0]['pages'][0]['status'] = 'target'
        self.assertIn('目标规格', docs.render_index(data, 'zh-CN'))
        data['groups'][0]['pages'][0]['source'] = '../outside.md'
        self.write_catalog(data)
        with self.assertRaisesRegex(ValueError, 'invalid source'):
            docs.load_catalog(self.root)

    def test_links_in_code_are_examples_not_navigation(self):
        body = '[broken](missing.md)\n`[example](also-missing.md)`\n```md\n[x](example.md)\n```\n[ok](CONTEXT.md)'
        self.assertEqual(docs.check_links(self.root, 'README.md', body), ['README.md: missing link target missing.md'])

    def test_command_typos_flags_continuations_and_native_args(self):
        commands = {'amux': ({'--json'}, False), 'amux session': (set(), False),
                    'amux session run': ({'--name'}, True)}
        self.assertEqual(docs.check_commands('guide.md', '```sh\namux session run builtin/codex \\\n  --name demo -- --native-option\n```', commands), [])
        for command in ['amux session typo', 'amux session run builtin/codex --typo demo']:
            self.assertTrue(docs.check_commands('guide.md', '```sh\n' + command + '\n```', commands))


if __name__ == '__main__':
    unittest.main()
