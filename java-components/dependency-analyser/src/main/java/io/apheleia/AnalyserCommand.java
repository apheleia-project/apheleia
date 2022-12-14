package io.apheleia;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.FileVisitResult;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.SimpleFileVisitor;
import java.nio.file.attribute.BasicFileAttributes;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import java.util.function.UnaryOperator;
import java.util.regex.Pattern;

import javax.enterprise.inject.Instance;
import javax.inject.Inject;

import org.cyclonedx.BomGeneratorFactory;
import org.cyclonedx.CycloneDxSchema;
import org.cyclonedx.generators.json.BomJsonGenerator;
import org.cyclonedx.model.Bom;
import org.cyclonedx.model.Component;
import org.cyclonedx.model.Property;

import com.redhat.hacbs.classfile.tracker.ClassFileTracker;
import com.redhat.hacbs.classfile.tracker.TrackingData;
import com.redhat.hacbs.resources.model.v1alpha1.ArtifactBuild;
import com.redhat.hacbs.resources.util.HashUtil;
import com.redhat.hacbs.resources.util.ResourceNameUtils;

import io.fabric8.kubernetes.client.KubernetesClient;
import io.fabric8.kubernetes.client.dsl.Resource;
import io.quarkus.logging.Log;
import io.quarkus.picocli.runtime.annotations.TopCommand;
import io.quarkus.runtime.Quarkus;
import picocli.CommandLine;

@TopCommand
@CommandLine.Command
public class AnalyserCommand implements Runnable {

    @Inject
    Instance<KubernetesClient> client;

    @CommandLine.Option(names = "--allowed-sources", defaultValue = "redhat,rebuilt", split = ",")
    Set<String> allowedSources;

    @CommandLine.Option(names = "--sbom-path", description = "The path to generate a SBOM at")
    Path sbom;

    @CommandLine.Option(names = "--maven-repo", required = true, description = "The path to the local .m2/repsitory directory. Usually $HOME/.m2/repository")
    Path mavenRepo;

    @CommandLine.Option(names = "--create-artifacts", description = "If the analyser should use the Kube API to create ArtifactBuild objects")
    boolean createArtifacts;

    @CommandLine.Option(names = "--allowed-artifacts", description = "A list of regexes of artifacts that are allowed to come from community sources")
    List<String> allowedArtifacts;

    @CommandLine.Parameters(description = "The paths to check for community artifacts. Can be files or directories.")
    List<Path> paths;

    @Override
    public void run() {
        try {
            Set<TrackingData> trackingData = new HashSet<>();
            Set<String> communityGavs = doAnalysis(trackingData);
            if (createArtifacts) {
                var c = client.get();
                var abrc = c.resources(ArtifactBuild.class);
                for (var i : communityGavs) {
                    String name = ResourceNameUtils.nameFromGav(i);
                    Log.infof("Creating/Updating %s", name);
                    Resource<ArtifactBuild> artifactBuildResource = abrc.withName(name);
                    var abr = artifactBuildResource.get();
                    if (abr == null) {
                        abr = new ArtifactBuild();
                        abr.getMetadata().setName(name);
                        abr.getSpec().setGav(i);
                        abr.getMetadata().setAnnotations(new HashMap<>());
                        abr.getMetadata().getAnnotations().put("aphelaia.io/last-used", "" + System.currentTimeMillis());
                        c.resource(abr).createOrReplace();
                    } else {
                        artifactBuildResource.edit(new UnaryOperator<ArtifactBuild>() {
                            @Override
                            public ArtifactBuild apply(ArtifactBuild artifactBuild) {
                                if (artifactBuild.getMetadata().getAnnotations() == null) {
                                    artifactBuild.getMetadata().setAnnotations(new HashMap<>());
                                }
                                artifactBuild.getMetadata().getAnnotations().put("aphelaia.io/last-used",
                                        "" + System.currentTimeMillis());
                                return artifactBuild;
                            }
                        });
                    }
                }
            }
            if (!communityGavs.isEmpty()) {
                //exit with non-zero if there were community deps
                Quarkus.asyncExit(1);
            } else {
                //note that the SBOM is only valid when there are no community deps
                writeSbom(trackingData);
            }
        } catch (Exception e) {
            throw new RuntimeException(e);
        }
    }

    Set<String> doAnalysis(Set<TrackingData> trackingData) throws IOException {
        Set<String> communityGavs = new HashSet<>();
        List<Pattern> allowedList = new ArrayList<>();
        if (this.allowedArtifacts != null) {
            for (var i : allowedArtifacts) {
                allowedList.add(Pattern.compile(i));
            }
        }
        //scan the local maven repo first

        //map of class name -> path -> hash
        Map<String, Map<Path, String>> untrackedCommunityClassesForMaven = new HashMap<>();
        Files.walkFileTree(mavenRepo, new SimpleFileVisitor<>() {
            @Override
            public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) throws IOException {
                if (file.getFileName().toString().endsWith(".jar") && !file.getFileName().toString().endsWith("-runner.jar")) {
                    ClassFileTracker.readTrackingDataFromJar(Files.readAllBytes(file), file.getFileName().toString(),
                            (s, b) -> {
                                if (s.equals("module-info")) {
                                    return;
                                }
                                untrackedCommunityClassesForMaven.computeIfAbsent(s, (a) -> new HashMap<>()).put(file,
                                        HashUtil.sha1(b));
                            });
                }
                return FileVisitResult.CONTINUE;
            }
        });
        //look for classes produced by the build and remove them from the community set
        for (var path : paths) {
            Files.walkFileTree(path, new SimpleFileVisitor<>() {
                @Override
                public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) throws IOException {
                    if (file.getFileName().toString().endsWith(".class")) {
                        ClassFileTracker.readTrackingInformationFromClass(Files.readAllBytes(file),
                                untrackedCommunityClassesForMaven::remove);
                    }
                    return FileVisitResult.CONTINUE;
                }
            });
        }

        Log.infof("Root paths %s", paths);

        Set<Path> additional = new HashSet<>();
        Set<Set<Path>> multiplesToResolve = new TreeSet<>(new Comparator<Set<Path>>() {
            @Override
            public int compare(Set<Path> o1, Set<Path> o2) {
                int v = Integer.compare(o1.size(), o2.size());
                if (v == 0) {
                    return o1.toString().compareTo(o2.toString());
                }
                return v;
            }
        });

        for (var path : paths) {
            Files.walkFileTree(path, new SimpleFileVisitor<>() {
                @Override
                public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) throws IOException {
                    var fileName = file.getFileName().toString();
                    try (var contents = Files.newInputStream(file)) {
                        Log.debugf("Processing %s", fileName);
                        var jarData = ClassFileTracker.readTrackingDataFromFile(contents, fileName, (s, b) -> {
                            if (untrackedCommunityClassesForMaven.containsKey(s)) {
                                var jars = untrackedCommunityClassesForMaven.get(s);
                                var filtered = new ArrayList<Path>();
                                if (jars.size() > 1) {
                                    var thisHash = HashUtil.sha1(b);
                                    for (var i : jars.entrySet()) {
                                        if (i.getValue().equals(thisHash)) {
                                            filtered.add(i.getKey());
                                        }
                                    }
                                    if (filtered.size() == 1) {
                                        for (var jar : filtered) {
                                            if (additional.add(jar)) {
                                                Log.infof("Community jar " + jar.getFileName() + " found in "
                                                        + path.relativize(file));
                                            }
                                        }
                                    } else if (filtered.size() > 1) {
                                        multiplesToResolve.add(new HashSet<>(filtered));
                                    } else {
                                        multiplesToResolve.add(new HashSet<>(jars.keySet()));
                                    }

                                } else {
                                    for (var jar : jars.entrySet()) {
                                        if (additional.add(jar.getKey())) {
                                            Log.infof("Community jar " + jar.getKey().getFileName() + " found in "
                                                    + path.relativize(file));
                                        }
                                    }
                                }
                            }

                        });
                        trackingData.addAll(jarData);
                        for (var data : jarData) {
                            if (data != null) {
                                if (!allowedSources.contains(data.source)) {
                                    Log.debugf("Found GAV %s in %s", data.gav, fileName);
                                    communityGavs.add(data.gav);
                                }
                            }
                        }

                    }
                    return FileVisitResult.CONTINUE;

                }
            });
        }
        for (var i : multiplesToResolve) {
            boolean alreadyResolved = false;
            for (var b : i) {
                if (additional.contains(b)) {
                    alreadyResolved = true;
                    break;
                }
            }
            if (!alreadyResolved) {
                Log.errorf("Unable to resolve a multi-jar situation, adding all versions %s", i);
                additional.addAll(i);
            }
        }
        //now figure out the additional GAV's
        for (var i : additional) {
            boolean gradle = i.getParent().getFileName().toString().length() == 40;
            //gradle repo layout is different to maven
            //we use a different strategy to determine the GAV
            if (gradle) {
                Path version = i.getParent().getParent();
                Path artifact = version.getParent();
                Path group = artifact.getParent();
                String gav = group.getFileName() + ":" + artifact.getFileName() + ":" + version.getFileName();
                communityGavs.add(gav);
                trackingData.add(new TrackingData(gav, "community", Map.of()));
            } else {
                Path version = i.getParent();
                Path artifact = version.getParent();
                var group = mavenRepo.relativize(artifact.getParent()).toString().replace("/", ".");
                String gav = group + ":" + artifact.getFileName() + ":" + version.getFileName();
                communityGavs.add(gav);
                trackingData.add(new TrackingData(gav, "community", Map.of()));
            }
        }

        var it = communityGavs.iterator();
        while (it.hasNext()) {
            var item = it.next();
            for (var i : allowedList) {
                if (i.matcher(item).matches()) {
                    Log.infof("Community dependency %s was allowed by specified pattern %s", item, i.pattern());
                    it.remove();
                }
            }
        }
        System.err.println(communityGavs);
        return communityGavs;
    }

    void writeSbom(Set<TrackingData> trackingData) throws IOException {
        //now build a cyclone DX bom file
        final Bom bom = new Bom();
        bom.setComponents(new ArrayList<>());
        for (var i : trackingData) {
            var split = i.gav.split(":");
            String group = split[0];
            String name = split[1];
            String version = split[2];

            Component component = new Component();
            component.setType(Component.Type.LIBRARY);
            component.setGroup(group);
            component.setName(name);
            component.setVersion(version);
            component.setPublisher(i.source);
            component.setPurl(String.format("pkg:maven/%s/%s@%s", group, name, version));

            Property packageTypeProperty = new Property();
            packageTypeProperty.setName("package:type");
            packageTypeProperty.setValue("maven");

            Property packageLanguageProperty = new Property();
            packageLanguageProperty.setName("package:language");
            packageLanguageProperty.setValue("java");

            component.setProperties(List.of(packageTypeProperty, packageLanguageProperty));

            bom.getComponents().add(component);
        }
        BomJsonGenerator generator = BomGeneratorFactory.createJson(CycloneDxSchema.Version.VERSION_14, bom);
        String sbom = generator.toJsonString();
        Log.infof("Generated SBOM:\n%s", sbom);
        if (this.sbom != null) {
            Files.writeString(this.sbom, sbom, StandardCharsets.UTF_8);
        }
    }
}
