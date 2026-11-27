/* Render the Bridge's management card as native browser controls. */
(function (root) {
  'use strict';
  function renderCard(doc, { e, t, language, invoke, disabled }) {
    const locale = language === 'zh-CN' ? 'zh_cn' : 'en_us';
    function copy(value) {
      if (!value) return '';
      return value.i18n_content?.[locale] ?? value.i18n_content?.en_us ?? value.content ?? '';
    }
    function link(url) {
      try {
        const parsed = new URL(url);
        return ['http:', 'https:'].includes(parsed.protocol) ? parsed.href : null;
      } catch (_) { return null; }
    }
    function rich(value) {
      return copy(value).split(/(\[[^\]\n]+\]\(https?:\/\/[^\s)]+\)|\*\*[^*]+\*\*)/g).map(part => {
        if (part.startsWith('**') && part.endsWith('**')) return e('strong', { text: part.slice(2, -2) });
        const match = part.match(/^\[([^\]]+)\]\((.+)\)$/);
        if (match && link(match[2])) return e('a', { href: link(match[2]), target: '_blank', rel: 'noopener noreferrer', text: match[1] });
        return part;
      });
    }
    function collect(form) {
      return form ? Object.fromEntries(Array.from(form.querySelectorAll('input[name],textarea[name],select[name]')).filter(input => !input.disabled && (input.tagName !== 'SELECT' || input.value !== '')).map(input => [input.name, input.value])) : {};
    }
    function control(n, form, option) {
      if (disabled || !n.control) return;
      if (n.action_type === 'form_submit' && form && !form.reportValidity()) return;
      if (n.confirm && !root.confirm([copy(n.confirm.title), copy(n.confirm.text)].filter(Boolean).join('\n'))) return;
      invoke(n.control, collect(form), option || '');
    }
    function node(n, form) {
      const children = () => (n.elements || n.actions || []).map(child => node(child, form));
      const name = n.name;
      const text = copy(n.text) || copy(n) || copy(n.placeholder);
      const fields = { name, required: Boolean(n.required), disabled: disabled || n.disabled, 'aria-label': copy(n.placeholder) || name };
      switch (n.tag) {
      case 'hr': return e('hr');
      case 'form': {
        const host = e('form', { class: 'workspace-form', onsubmit: ev => ev.preventDefault() });
        host.append(...(n.elements || []).map(child => node(child, host)));
        return host;
      }
      case 'column_set': return e('div', { class: 'workspace-columns' }, (n.columns || []).map(col => node(col, form)));
      case 'column': return e('div', { class: 'workspace-column' + (n.width === 'auto' ? ' auto' : '') }, children());
      case 'collapsible_panel': return e('details', { class: 'workspace-panel', open: n.expanded }, e('summary', { text: copy(n.header?.title) }), children());
      case 'select_static':
        return e('select', Object.assign(fields, { onchange: ev => { if (n.control) control(n, form, ev.target.value); } }),
          !n.initial_option ? e('option', { value: '', text: copy(n.placeholder) || t('chooseOption') }) : null,
          (n.options || []).map(opt => e('option', { value: opt.value, selected: opt.value === n.initial_option, text: copy(opt.text) })));
      case 'input':
        return e(n.input_type === 'multiline_text' ? 'textarea' : 'input', Object.assign(fields, {
          type: /secret|password|api[_-]?key|access[_-]?token/i.test(name || '') ? 'password' : 'text',
          autocomplete: 'off', value: n.default_value || '', placeholder: copy(n.placeholder)
        }));
      case 'button':
      case 'interactive_container': {
        const content = n.tag === 'button' ? text : children();
        const url = link(n.url);
        const cls = n.tag === 'interactive_container' ? 'workspace-tile' : 'ui-btn ' + (n.type === 'danger' ? 'danger' : n.type === 'primary' ? 'primary' : '');
        if (url) return e('a', { class: cls, href: url, rel: 'noopener noreferrer', target: '_blank' }, content);
        return e('button', { type: 'button', class: cls, disabled: disabled || n.disabled || !n.control, onclick: () => control(n, form) }, content);
      }
      case 'markdown': case 'plain_text': case 'div':
        return e('div', { class: 'workspace-text ' + (n.text_size === 'heading' ? 'heading' : n.text_size === 'notation' ? 'notation' : '') }, rich(n.text || n), children());
      default: return e('div', {}, text, children());
      }
    }
    return e('section', { class: 'panel workspace-card' },
      e('header', { class: 'panel-head' }, e('div', {}, e('h2', { text: copy(doc.header?.title) || '/amux' }), doc.header?.subtitle ? e('p', { class: 'cell-sub', text: copy(doc.header.subtitle) }) : null)),
      e('div', { class: 'panel-body' }, (doc.body?.elements || []).map(element => node(element, null))));
  }
  root.ConsoleWorkspace = { renderCard };
})(typeof window === 'undefined' ? globalThis : window);
