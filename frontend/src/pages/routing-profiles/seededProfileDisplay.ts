import type { RoutingProfile } from '../../entities/routingProfile/api/routingProfileApi';

// Legacy built-in record, seeded by migration 000060. The API stores profile
// metadata verbatim; "isDefault" alone is NOT proof of a built-in profile.
const SEEDED_NAME = 'Default direct';
const SEEDED_DESCRIPTION = 'Built-in default profile. All traffic uses the VPN route unless explicit split-tunnel rules are added.';

type ProfileMetadata = Pick<RoutingProfile, 'name' | 'description'>;
type SeededLabelKey = 'routingProfiles.seededDefaultName' | 'routingProfiles.seededDefaultDescription';

export function isSeededRoutingProfile(profile: ProfileMetadata): boolean {
  return profile.name === SEEDED_NAME && profile.description === SEEDED_DESCRIPTION;
}

/** Display localization only. Never mutate API objects or persisted metadata. */
export function displayRoutingProfile(
  profile: ProfileMetadata,
  translate: (key: SeededLabelKey) => string,
): { name: string; description: string | null | undefined } {
  if (!isSeededRoutingProfile(profile)) {
    return { name: profile.name, description: profile.description };
  }
  return {
    name: translate('routingProfiles.seededDefaultName'),
    description: translate('routingProfiles.seededDefaultDescription'),
  };
}
