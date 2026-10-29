import React from 'react';

interface RoutineOpsLogoProps {
  size?: number | string;
  className?: string;
}

// Знак раздаётся из brand/ как растр (SVG-истока у него нет), поэтому не инлайним,
// а тянем /logo.png — он же лежит в web/public, см. brand/sync-icons.sh.
export const RoutineOpsLogo: React.FC<RoutineOpsLogoProps> = ({ size = 200, className }) => (
  <img
    src="/logo.png"
    width={size}
    height={size}
    className={className}
    alt="RoutineOps"
  />
);
