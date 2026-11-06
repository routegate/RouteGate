import assert from 'node:assert/strict';
import test from 'node:test';
import { displayRoutingProfile, isSeededRoutingProfile } from './seededProfileDisplay.ts';

const seeded = {
  name: 'Default direct',
  description: 'Built-in default profile. All traffic uses the VPN route unless explicit split-tunnel rules are added.',
};
const ru = key => ({
  'routingProfiles.seededDefaultName': 'Базовый профиль',
  'routingProfiles.seededDefaultDescription': 'Встроенный базовый профиль. Весь трафик проходит через VPN, если не заданы отдельные правила раздельной маршрутизации.',
})[key];
const en = key => ({
  'routingProfiles.seededDefaultName': seeded.name,
  'routingProfiles.seededDefaultDescription': seeded.description,
})[key];

test('legacy seeded default displays Russian translations without changing stored data', () => {
  assert.equal(isSeededRoutingProfile(seeded), true);
  const localized = displayRoutingProfile(seeded, ru);
  assert.equal(localized.name, 'Базовый профиль');
  assert.match(localized.description, /Весь трафик проходит через VPN/);
  assert.equal(seeded.name, 'Default direct');
  assert.equal(seeded.description.startsWith('Built-in'), true);
  assert.notEqual(localized, seeded);
});

test('English mode preserves the original seed exactly', () => {
  assert.deepEqual(displayRoutingProfile(seeded, en), seeded);
});

test('custom default profiles and edited seed metadata remain operator-owned text', () => {
  const profiles = [
    { name: 'Runet', description: 'Runet' },
    { name: 'Default direct', description: 'Custom description' },
    { name: 'My direct route', description: seeded.description },
    { name: 'Default direct', description: null },
  ];
  for (const profile of profiles) {
    assert.equal(isSeededRoutingProfile(profile), false);
    assert.deepEqual(displayRoutingProfile(profile, ru), profile);
  }
});

test('isDefault must never be treated as a localization identifier', () => {
  const custom = { name: 'Runet', description: 'My traffic policy', isDefault: true };
  const seededNoLongerDefault = { ...seeded, isDefault: false };
  assert.deepEqual(displayRoutingProfile(custom, ru), { name: 'Runet', description: 'My traffic policy' });
  assert.equal(displayRoutingProfile(seededNoLongerDefault, ru).name, 'Базовый профиль');
});
