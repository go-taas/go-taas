// Live accelerator catalog for the console forms.
//
// The vendors and card types an operator may pick come from the cluster,
// not from a constant: the accelerator inventory reports what the nodes
// actually carry, so a fleet that grows a vendor (or retires a card) is
// reflected without a console change. A write-time constant would let an
// operator request hardware that does not exist and wait for an
// unschedulable pod to explain it.

import { useCallback, useEffect, useState } from 'react';
import { api } from '../api';

export interface FleetCardType {
  vendor: string;
  cardType: string;
  total: number;
  free: number;
  nodeCount: number;
}

interface CardTypesResponse {
  response: { code: number; message: string };
  cardTypes?: FleetCardType[];
}

// CPU_ACCELERATOR is the marker an image uses when it needs no
// accelerator; it is always offered because a CPU-only image can run on
// any node.
export const CPU_ACCELERATOR = 'cpu';

export interface FleetAccelerators {
  /** Vendors present in the fleet, lowercased and deduplicated. */
  vendors: string[];
  /** Every card type with its owning vendor. */
  cardTypes: FleetCardType[];
  /** Card types of one vendor, for the dependent select. */
  cardTypesFor: (vendor: string) => FleetCardType[];
  loading: boolean;
  reload: () => void;
}

// useFleetAccelerators reads the live card-type summary and derives the
// vendor list from it.
export function useFleetAccelerators(orgId: string): FleetAccelerators {
  const [cardTypes, setCardTypes] = useState<FleetCardType[]>([]);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.get<CardTypesResponse>(
        '/api/v1/admin/accelerators/card-types?page.limit=200',
        orgId,
      );
      setCardTypes(data.cardTypes || []);
    } catch {
      // No inventory (a controller-less install, or the caller cannot read
      // it): the forms fall back to the CPU marker alone rather than
      // silently offering hardware nobody verified.
      setCardTypes([]);
    } finally {
      setLoading(false);
    }
  }, [orgId]);

  useEffect(() => {
    void load();
  }, [load]);

  const vendors: string[] = [];
  for (const ct of cardTypes) {
    const v = (ct.vendor || '').toLowerCase();
    if (v && !vendors.includes(v)) vendors.push(v);
  }
  vendors.sort();

  const cardTypesFor = (vendor: string) =>
    cardTypes.filter((ct) => (ct.vendor || '').toLowerCase() === vendor.toLowerCase());

  return { vendors, cardTypes, cardTypesFor, loading, reload: () => void load() };
}
