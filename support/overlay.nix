{ pkgs
}: (final: prev: {
  setup-envtest = pkgs.callPackage ./setup-envtest.nix { };
  vcluster = pkgs.callPackage ./vcluster.nix { };
})
