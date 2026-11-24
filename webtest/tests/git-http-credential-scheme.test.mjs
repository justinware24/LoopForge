import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// A self-hosted GitLab commonly serves its API over plain HTTP, so the form
// offers the scheme the credential is bound to.
test('A Git credential can be bound to plain HTTP for a self-hosted instance', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const endpoint = '/api/users/1/git-http-credentials';
  let creds = [];
  app.router.get(endpoint, () => creds);
  app.router.put(endpoint, ({ body }) => {
    creds = [...creds, { id: 'corp', name: body.name, platform: body.platform, host: body.host, scheme: body.scheme, username: body.username }];
    return creds;
  });
  app.evaluate(`
    state.currentUser={...state.data.users[0],id:1,isGuest:false,gitHttpCredentialsConfigured:true};
    state.data.users=[state.currentUser];
    renderPersonalSettings();
    openCredentialWizard();
  `);
  const wizard = app.document.querySelector('#credentialWizard');
  wizard.querySelector('[data-wizard-kind="gitHttpCredentials"]').click();
  wizard.querySelector('[data-wizard-next]').click();
  await settle(6);

  const dialog = app.document.querySelector('#gitHTTPCredentialDialog');
  const form = dialog.querySelector('form');
  assert.equal(form.elements.scheme.value, 'https', 'the form defaults to https');
  assert.equal(form.elements.scheme.options.length, 2);

  form.elements.host.value = 'gitlab.corp.example:8929';
  form.elements.host.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  form.elements.scheme.value = 'http';
  form.elements.name.value = 'Corp GitLab';
  form.elements.token.value = 'glpat-test-only';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(6);

  const saves = () => app.router.calls.filter(call => call.method === 'PUT' && call.path === endpoint);
  assert.equal(saves()[0].body.scheme, 'http');
  assert.equal(saves()[0].body.host, 'gitlab.corp.example:8929');
  assert.match(dialog.querySelector('.git-ssh-key-list').textContent, /HTTP/, 'the list marks the plain HTTP credential');
});
